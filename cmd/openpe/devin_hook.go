package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/adapters/delivery"
	devinadapter "github.com/AoManoh/openpe/internal/adapters/devin"
	"github.com/AoManoh/openpe/internal/adapters/hookdedup"
	"github.com/AoManoh/openpe/internal/config"
	devinhistory "github.com/AoManoh/openpe/internal/context/devinhistory"
	devinsession "github.com/AoManoh/openpe/internal/context/devinsession"
	"github.com/AoManoh/openpe/internal/context/histstatus"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers"
)

func runDevin(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) > 0 && args[0] == "hook" {
		return runDevinHook(args[1:], stdin, stdout, stderr, newProvider, getwd)
	}
	fmt.Fprintf(stderr, "unknown devin command\n")
	printDevinHookUsage(stderr)
	return 2
}

func runDevinHook(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) == 0 {
		printDevinHookUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return runDevinHookRun(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "install":
		return runDevinHookInstall(args[1:], stdout, stderr, getwd)
	case "last":
		return runDevinHookLast(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printDevinHookUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown devin hook command: %s\n", args[0])
		printDevinHookUsage(stderr)
		return 2
	}
}

func runDevinHookLast(args []string, stdout io.Writer, stderr io.Writer) int {
	return runDeliveryLast("devin hook last", "devin", args, stdout, stderr)
}

func runDevinHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("devin hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "devin", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	auto := fs.Bool("auto", false, "enhance every prompt and inject it as additional context")
	// Default stderr: the Devin CLI only documents exit-2 as the hook block
	// channel (reason read from stderr since 3000.3.22); a stdout
	// {"decision":"block"} on UserPromptSubmit is not honoured — the 2026-08-03
	// incident let an enhanced-and-"blocked" `pe` prompt sail through to the
	// model. "json" remains available for testing and older hosts.
	blockOutput := fs.String("block-output", "stderr", "preview block output: stderr (exit 2, Devin-documented) or json (legacy stdout decision)")
	// The host kills a hook that outruns its configured timeout (120s by
	// install default) WITHOUT reading any output — on 2026-08-03 a clipboard
	// hang ate the whole budget and the raw `pe` prompt reached the model. The
	// deadline keeps the worst case inside our own hands: expire BEFORE the
	// host does and still emit a proper block.
	hookDeadline := fs.Duration("deadline", cfg.HookDeadline, "overall positive hook deadline; on expiry a manual pe is blocked before the host can kill the hook")
	terminalPreview := fs.Bool("terminal-preview", envBoolOrDefault("OPENPE_DEVIN_TERMINAL_PREVIEW", false), "experimental: write full Markdown preview directly to /dev/tty when blocking")
	copyPreview := fs.Bool("copy-preview", envBoolOrDefault("OPENPE_DEVIN_COPY_PREVIEW", true), "copy enhanced prompt to the system clipboard when blocking")
	hookScope := fs.String("hook-scope", envOrDefault("OPENPE_HOOK_SCOPE", ""), "hook scope for duplicate suppression: user or project")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookDeadline <= 0 {
		fmt.Fprintln(stderr, "deadline must be positive")
		return 2
	}
	input, err := devinadapter.DecodeHookInput(stdin)
	if err != nil {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.Skip(localizedInvalidDevinHookInput(err, cfg.Language)))
	}
	manualTrigger, shouldHandle := devinadapter.ShouldHandleHook(input, *auto)
	if !shouldHandle {
		return 0
	}
	// A project-scope hook is redundant when a user-scope openPE hook already
	// exists, so suppress it to avoid enhancing the same prompt twice.
	if strings.TrimSpace(*hookScope) == "project" && devinadapter.HasUserOpenPEHookConfig() {
		return 0
	}
	// The session identity feeds both the de-dup claim key (same text in two
	// parallel sessions must not dedup against each other) and the history
	// lookup, so discover it once up front. The cwd joins the claim key as the
	// fallback identity when no session id is discoverable.
	// Devin's UserPromptSubmit stdin does not carry cwd; fall back to
	// DEVIN_PROJECT_DIR (set for command hooks) then the process cwd.
	sessionID := discoverDevinSessionID(cfg.Devin.History.DBPath)
	overrideCWD := strings.TrimSpace(input.CWD)
	if overrideCWD == "" {
		overrideCWD = strings.TrimSpace(os.Getenv("DEVIN_PROJECT_DIR"))
	}
	if overrideCWD == "" {
		workingDir, err := getwd()
		if err != nil {
			return emitDevinOutput(devinadapter.HookError(manualTrigger, fmt.Sprintf("get cwd: %v", err), cfg.Language), *blockOutput, false, stdout, stderr)
		}
		overrideCWD = workingDir
	}
	// Cross-adapter single-flight: Devin also imports the Claude- and
	// Windsurf-format openPE hooks and runs EVERY one of them for a single
	// prompt (a block does not short-circuit the rest). The first claims the
	// prompt and enhances it; later firings inside the window mirror the
	// winner's conclusion: after a review block they replay the block — the
	// winner's recorded disclosure notes plus its claim-bound enhancement —
	// because Devin displays the LAST hook's block reason, and a plain skip
	// here once passed a raw resubmitted `pe` prompt through to the model.
	// After an injection they skip so the prompt is injected exactly once.
	var claimConclusion hookdedup.Conclusion
	if cfg.HookDedup.Enabled {
		won, prior, finish := hookdedup.Claim(cfg.Delivery.CacheDir, devinDedupKey(sessionID, overrideCWD, input.Prompt), cfg.HookDedup.Window)
		if !won {
			return handleDevinDedupLoss(cfg, prior, manualTrigger, *copyPreview, *blockOutput, *terminalPreview, stdout, stderr)
		}
		defer func() { finish(claimConclusion) }()
	}
	// The flight computes everything and only the winning path below emits, so
	// the deadline watchdog can abandon it without racing on the writers.
	flight := func(ctx context.Context) devinFlightResult {
		provider, err := newProvider(providers.Spec{
			Provider:  cfg.Provider,
			MaxTokens: cfg.MaxTokens,
			BaseURL:   baseURL.ValueOrDefault(cfg.BaseURL),
			APIKey:    apiKey.ValueOrDefault(cfg.APIKey),
			Model:     model.ValueOrDefault(cfg.Model),
			Timeout:   *timeout,
		})
		if err != nil {
			return devinFlightResult{Output: devinadapter.HookError(manualTrigger, fmt.Sprintf("configure provider: %v", err), cfg.Language)}
		}
		service, err := newEnhancerService(provider, cfg)
		if err != nil {
			return devinFlightResult{Output: devinadapter.HookError(manualTrigger, fmt.Sprintf("configure enhancer: %v", err), cfg.Language)}
		}
		hist, histErr := devinSessionHistory(input.Prompt, overrideCWD, cfg, sessionID)
		output, err := devinadapter.HandleHook(ctx, service, input, devinadapter.HookOptions{
			Client:   *client,
			Mode:     *mode,
			Auto:     *auto,
			CWD:      overrideCWD,
			Language: cfg.Language,
			Timeout:  timeoutOrDefault(*timeout),
			History:  hist.Messages,
			// Default false: hold a manual `pe` for review (block + show the enhanced
			// prompt) so the user controls what is sent. The unified inject switch
			// (OPENPE_HOOK_INJECT / OPENPE_DEVIN_INJECT, resolved in config) is the
			// opt-in "I trust it" mode. --auto is inherently inject — it would be
			// unusable if it blocked every prompt.
			Inject:           *auto || cfg.Inject.Devin,
			MaxContextTokens: cfg.MaxContextTokens,
			CacheDir:         cfg.Delivery.CacheDir,
		})
		if err != nil {
			return devinFlightResult{Output: devinadapter.HookError(manualTrigger, err.Error(), cfg.Language)}
		}
		result := devinFlightResult{Outcome: dedupOutcomeFor(output)}
		result.Output, result.Notes = applyDevinDisclosure(output, hist, histErr, cfg.Language)
		return result
	}
	result := runDevinFlightWithDeadline(flight, *hookDeadline, manualTrigger, cfg.Language)
	if *copyPreview {
		result.Output = deliverDevinBlock(cfg, result.Output, result.Notes)
	}
	claimConclusion = devinClaimConclusion(result)
	output := result.Output
	if output.Decision == "block" && *blockOutput != "stderr" && *blockOutput != "json" {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(true, fmt.Sprintf("unsupported block-output %q", *blockOutput), cfg.Language))
	}
	return emitDevinOutput(output, *blockOutput, *terminalPreview, stdout, stderr)
}

// devinClaimConclusion converts a finished flight into what the de-dup claim
// records for losers. EVERY block — successful review blocks and error /
// deadline blocks alike — is recorded as OutcomeBlock: an error block has no
// prompt to re-deliver, but its interception must still be replayable. It
// used to be recorded as OutcomeUnknown, and a manual retry of the same text
// inside the freshness window skipped straight through to the model.
func devinClaimConclusion(result devinFlightResult) hookdedup.Conclusion {
	outcome := result.Outcome
	if outcome == hookdedup.OutcomeUnknown && result.Output.Decision == "block" {
		outcome = hookdedup.OutcomeBlock
	}
	return hookdedup.Conclusion{
		Outcome: outcome,
		Notes:   result.Notes,
		Prompt:  result.Output.PreviewPrompt,
		Reason:  result.Output.Reason,
	}
}

// handleDevinDedupLoss decides a losing hook firing. A recorded block is
// replayed (never letting the resubmitted `pe` text through), a recorded
// injection is skipped (the prompt already proceeded enhanced), and an
// UNKNOWN conclusion fails closed for manual triggers: the winner is still
// in flight or crashed, and letting the raw trigger text pass "just in case"
// is exactly the fail-open the 2026-08-04 review reproduced. Auto/inject
// firings keep skipping so ordinary prompts are never held hostage.
func handleDevinDedupLoss(cfg config.Config, prior hookdedup.Prior, manualTrigger bool, copyPreview bool, blockOutput string, terminalPreview bool, stdout io.Writer, stderr io.Writer) int {
	switch prior.Outcome {
	case hookdedup.OutcomeBlock:
		return replayDevinBlockedPrompt(cfg, prior, copyPreview, blockOutput, terminalPreview, stdout, stderr)
	case hookdedup.OutcomeInject:
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.SkipOutput())
	default:
		if manualTrigger {
			out := devinadapter.Block(localizedDedupUnknownClaim(cfg.Language))
			return emitDevinBlock(out, blockOutput, terminalPreview, stdout, stderr)
		}
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.SkipOutput())
	}
}

// localizedDedupUnknownClaim explains a fail-closed loss: an identical prompt
// was claimed moments ago but no conclusion is recorded yet (winner still in
// flight or crashed). The manual trigger must not pass through raw.
func localizedDedupUnknownClaim(language string) string {
	if isEnglishLanguage(language) {
		return "openPE: an identical prompt is still being enhanced (or its outcome was lost); the original message was NOT submitted. Wait a few seconds and retry."
	}
	return "openPE：相同内容的增强仍在进行（或上一次结论丢失），原始消息未提交。请稍候几秒后重试。"
}

// devinFlightResult is a fully computed hook conclusion: the output to emit
// plus what the de-dup claim should record about the flight.
type devinFlightResult struct {
	Output  devinadapter.HookOutput
	Outcome hookdedup.Outcome
	Notes   string
}

// runDevinFlightWithDeadline guards a hook flight with an overall deadline.
// The host kills an overrunning hook without reading any of its output, which
// silently un-does the interception (2026-08-03: a clipboard hang held the
// hook past the host's 120s timeout and the raw `pe` prompt reached the
// model). Expiring first keeps the conclusion ours: a manual trigger is
// blocked with an explanation, an auto/inject run degrades to a skip so
// ordinary prompts are never held hostage. The abandoned flight goroutine
// only computes values and touches no shared writer, so leaking it is safe.
func runDevinFlightWithDeadline(flight func(context.Context) devinFlightResult, deadline time.Duration, manualTrigger bool, language string) devinFlightResult {
	if deadline <= 0 {
		return flight(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	results := make(chan devinFlightResult, 1)
	go func() { results <- flight(ctx) }()
	select {
	case result := <-results:
		return result
	case <-ctx.Done():
		return devinFlightResult{Output: devinadapter.HookError(manualTrigger, localizedDevinHookDeadline(language, deadline), language)}
	}
}

// localizedDevinHookDeadline explains a deadline expiry. For a manual `pe` it
// becomes the block reason (the message the user acts on), so it must state
// plainly that nothing was submitted and how to retry.
func localizedDevinHookDeadline(language string, deadline time.Duration) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("enhancement did not finish within %s; the original message was NOT submitted. Retry, or raise OPENPE_HOOK_DEADLINE.", deadline)
	}
	return fmt.Sprintf("增强未在 %s 内完成，原始消息未提交。可重试或调大 OPENPE_HOOK_DEADLINE。", deadline)
}

// runningUnderDevin reports whether this hook process was invoked by the Devin
// CLI. Devin sets DEVIN_PROJECT_DIR for every command hook it runs — including
// the Claude- and Windsurf-format hooks it imports — so it is the reliable
// signal that the codex/claude/windsurf adapters must render Devin-native JSON
// output instead of their exit-2 + stderr delivery, which Devin misreads as an
// empty "Prompt blocked:".
func runningUnderDevin() bool {
	return strings.TrimSpace(os.Getenv("DEVIN_PROJECT_DIR")) != ""
}

// renderDevinHook enhances rawPrompt and emits Devin-native output (exit-2 +
// stderr reason for blocks — the only block channel Devin documents for hooks
// — and stdout additionalContext JSON for injections), applying cross-adapter
// single-flight de-duplication. It is shared by the claude/windsurf hook runs
// when the Devin CLI imports and invokes them, so a Claude- or
// Windsurf-installed hook still produces a correct single Devin enhancement.
// rawPrompt is the unparsed user text (still carrying the `pe` trigger); the
// Devin adapter parses it.
func renderDevinHook(cfg config.Config, service *enhancer.Service, rawPrompt string, cwd string, deadline time.Duration, stdout io.Writer, stderr io.Writer) int {
	sessionID := discoverDevinSessionID(cfg.Devin.History.DBPath)
	if devinProject := strings.TrimSpace(os.Getenv("DEVIN_PROJECT_DIR")); devinProject != "" {
		cwd = devinProject
	}
	_, _, manualTrigger := devinadapter.ParseManualEnhance(rawPrompt)
	var claimConclusion hookdedup.Conclusion
	if cfg.HookDedup.Enabled {
		won, prior, finish := hookdedup.Claim(cfg.Delivery.CacheDir, devinDedupKey(sessionID, cwd, rawPrompt), cfg.HookDedup.Window)
		if !won {
			return handleDevinDedupLoss(cfg, prior, manualTrigger, true, "stderr", false, stdout, stderr)
		}
		defer func() { finish(claimConclusion) }()
	}
	flight := func(ctx context.Context) devinFlightResult {
		// Devin 下无论由哪种格式的 hook 触发，都只读取当前 Devin session。
		hist, histErr := devinSessionHistory(rawPrompt, cwd, cfg, sessionID)
		output, err := devinadapter.HandleHook(ctx, service, devinadapter.HookInput{
			HookEventName: devinadapter.UserPromptSubmit,
			Prompt:        rawPrompt,
			CWD:           cwd,
		}, devinadapter.HookOptions{
			Client:           "devin",
			Mode:             "agent",
			Language:         cfg.Language,
			Timeout:          timeoutOrDefault(cfg.Timeout),
			History:          hist.Messages,
			Inject:           cfg.Inject.Devin,
			MaxContextTokens: cfg.MaxContextTokens,
			CacheDir:         cfg.Delivery.CacheDir,
		})
		if err != nil {
			return devinFlightResult{Output: devinadapter.HookError(manualTrigger, err.Error(), cfg.Language)}
		}
		result := devinFlightResult{Outcome: dedupOutcomeFor(output)}
		result.Output, result.Notes = applyDevinDisclosure(output, hist, histErr, cfg.Language)
		return result
	}
	result := runDevinFlightWithDeadline(flight, deadline, manualTrigger, cfg.Language)
	result.Output = deliverDevinBlock(cfg, result.Output, result.Notes)
	claimConclusion = devinClaimConclusion(result)
	return emitDevinOutput(result.Output, "stderr", false, stdout, stderr)
}

// applyDevinDisclosure folds the non-silent notes into the hook output: the
// history disclosure (whether prior Devin-session context was included, and
// whether it contains a compaction summary) plus any enhancer content warnings
// (out-of-context numbers / undecided actions — they exist precisely to be
// seen before the user acts). Block outputs carry the notes in Reason,
// inject/skip outputs in SystemMessage. The joined notes are also returned so
// a de-dup winner can record them for its losers: Devin runs every hook and
// displays the LAST block reason, so a loser's replayed block must be able to
// show the same disclosure the winner produced.
func applyDevinDisclosure(output devinadapter.HookOutput, hist devinhistory.Result, histErr error, language string) (devinadapter.HookOutput, string) {
	joined := disclosureNotes(hist.Messages, hist.Status, hist.SummaryCount, histErr, output.Warnings, language)
	if joined == "" {
		return output, ""
	}
	if output.Decision == "block" {
		output.Reason = strings.TrimSpace(joined + " " + output.Reason)
	} else {
		output.SystemMessage = strings.TrimSpace(joined + " " + output.SystemMessage)
	}
	return output, joined
}

// devinDedupKey scopes the de-dup claim to the current Devin session when its
// identity is known. The same kickoff text pasted into two parallel sessions
// within the window is two DIFFERENT requests (each must be enhanced with its
// own session context), while duplicate firings inside one session — sibling
// hooks of one submission, or an immediate resubmission — still share a claim.
// With no session identity (non-Linux / discovery failure) it falls back to
// cwd + text: weaker than a session id, but it still keeps two workspaces
// from claiming (and replaying) each other's flights.
func devinDedupKey(sessionID string, cwd string, prompt string) string {
	if strings.TrimSpace(sessionID) == "" {
		return "cwd:" + strings.TrimSpace(cwd) + "\x00" + prompt
	}
	return "session:" + sessionID + "\x00" + prompt
}

// deliverDevinBlock copies the enhanced prompt to the clipboard and sets the
// block reason from the shared delivery.HookStatus, so every review-mode Devin
// path (the native devin hook and the imported claude/windsurf hooks) shows the
// same "blocked + copied, paste it" / "clipboard failed, see hook last" feedback
// as the Codex/Claude/Windsurf clients. notes is the flight's disclosure
// (history note + enhancer warnings): the delivery status REPLACES the reason,
// so it must be re-prefixed here — the 2026-08-05 deadline rework ran delivery
// after the disclosure fold and silently dropped every warning from the block
// feedback (2026-08-10 review, BUG-001). It is a no-op for inject/skip outputs.
func deliverDevinBlock(cfg config.Config, output devinadapter.HookOutput, notes string) devinadapter.HookOutput {
	if output.Decision != "block" || strings.TrimSpace(output.PreviewPrompt) == "" {
		return output
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "devin"))
	output.Reason = strings.TrimSpace(strings.TrimSpace(notes) + " " + delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("devin")))
	return output
}

// dedupOutcomeFor classifies a finished hook flight for the de-dup claim.
// Only a successful review block (an enhanced preview exists) is replayable
// by a loser; error blocks and empty results stay OutcomeUnknown so losers
// fall back to a plain skip instead of replaying a cache that does not match.
func dedupOutcomeFor(output devinadapter.HookOutput) hookdedup.Outcome {
	switch {
	case output.Decision == "block" && strings.TrimSpace(output.PreviewPrompt) != "":
		return hookdedup.OutcomeBlock
	case output.HookSpecificOutput != nil && strings.TrimSpace(output.HookSpecificOutput.AdditionalContext) != "":
		return hookdedup.OutcomeInject
	default:
		return hookdedup.OutcomeUnknown
	}
}

// replayDevinBlockedPrompt re-emits a review block for a de-dup loser whose
// winner blocked this same prompt moments ago. The 2026-07-03 incident showed
// a plain skip here betrays the interception: the user resubmits the same
// `pe` text right after the block (e.g. because the clipboard paste failed)
// and the raw trigger message sails through to the model. Replaying costs no
// model call: the winner recorded its enhanced prompt in the claim body, so
// it is re-delivered (clipboard included) exactly like the original block.
// The claim — not the per-client "last prompt" cache — is the source: the
// global cache may already hold a PARALLEL session's enhancement by the time
// this loser fires (CR-003), and replaying that would leak another
// workspace's instructions into this one.
//
// The replayed reason = the winner's recorded disclosure notes + a FRESH
// delivery status. Devin runs every hook for one submission and displays the
// LAST block reason, so for sibling hooks this output IS what the user sees —
// it must read exactly like the winner's (a "duplicate submission" label here
// once accused a first-time prompt of being a resubmission and hid the
// history disclosure).
func replayDevinBlockedPrompt(cfg config.Config, prior hookdedup.Prior, copyPreview bool, blockOutput string, terminalPreview bool, stdout io.Writer, stderr io.Writer) int {
	prompt := strings.TrimSpace(prior.Prompt)
	if prompt == "" {
		// The winner blocked WITHOUT an enhanced prompt — an error or deadline
		// block. Replay its exact reason so the retry reads like the original
		// failure; with no recorded reason (older claim format or write
		// failure), still block with the degraded message, which must NOT
		// claim anything was reused.
		if reason := strings.TrimSpace(prior.Reason); reason != "" {
			return emitDevinBlock(devinadapter.Block(reason), blockOutput, terminalPreview, stdout, stderr)
		}
		out := devinadapter.Block(localizedDedupReplayCacheMiss(cfg.Language))
		return emitDevinBlock(out, blockOutput, terminalPreview, stdout, stderr)
	}
	out := devinadapter.BlockPreview(devinadapter.PreviewReason("", cfg.Language), devinadapter.MarkdownPreview(prompt, cfg.Language), prompt)
	if copyPreview {
		out = deliverDevinBlock(cfg, out, prior.Notes)
	} else {
		out.Reason = strings.TrimSpace(prior.Notes + " " + out.Reason)
	}
	return emitDevinBlock(out, blockOutput, terminalPreview, stdout, stderr)
}

// emitDevinOutput routes a finished hook output to the correct Devin channel:
// blocks through emitDevinBlock (exit-2 + stderr reason by default — the only
// block channel the Devin CLI documents), everything else (skip / injection)
// as Devin-native stdout JSON.
func emitDevinOutput(output devinadapter.HookOutput, blockOutput string, terminalPreview bool, stdout io.Writer, stderr io.Writer) int {
	if output.Decision == "block" {
		return emitDevinBlock(output, blockOutput, terminalPreview, stdout, stderr)
	}
	return devinadapter.EncodeHookOutputOrFallback(stdout, output)
}

// emitDevinBlock renders a block output the same way the native devin hook
// tail does: exit-2 stderr when requested, Devin-native JSON otherwise.
func emitDevinBlock(output devinadapter.HookOutput, blockOutput string, terminalPreview bool, stdout io.Writer, stderr io.Writer) int {
	if blockOutput == "stderr" {
		if terminalPreview {
			_ = devinadapter.WriteTerminalPreview(output.TerminalPreview)
		}
		fmt.Fprintln(stderr, output.Reason)
		return 2
	}
	return devinadapter.EncodeHookOutputOrFallback(stdout, output)
}

// localizedDedupReplayCacheMiss covers the degraded replay: the winner
// blocked this text moments ago but the cached enhancement could not be read
// back, so nothing is reused — the message must say so instead of claiming a
// reuse, and must stay neutral about WHY the hook fired again (a sibling hook
// of the same submission is indistinguishable from a quick resubmission).
func localizedDedupReplayCacheMiss(language string) string {
	if isEnglishLanguage(language) {
		return "openPE: this message was just enhanced and blocked, but the cached enhancement could not be read back; the original message was NOT submitted. Run `openpe devin hook last` to inspect the cache."
	}
	return "openPE：该消息刚刚已增强并拦截，但缓存的增强结果读取失败；原始消息未提交。可运行 openpe devin hook last 查看缓存。"
}

// devinSessionHistory returns the current Devin session history to inject when
// running under the Devin CLI. Like the codex/claude helpers, the error is
// non-nil only on a genuine read failure; "no session", "stale", "empty" and
// "ambiguous" are reported via the status so the hook layer surfaces them
// explicitly. On Linux the session is identified exactly from the hook's
// process ancestry (the devin ancestor holds session_locks/<id>.lock open),
// which makes same-directory parallel sessions safe; elsewhere — or if
// discovery fails — the collector falls back to the cwd+recency heuristic,
// which refuses to guess between multiple live sessions (Ambiguous).
func devinSessionHistory(prompt string, cwd string, cfg config.Config, sessionID string) (devinhistory.Result, error) {
	if !cfg.Devin.History.Enabled {
		return devinhistory.Result{Status: histstatus.Unknown}, nil
	}
	return devinhistory.New(devinhistory.Options{
		DBPath:      cfg.Devin.History.DBPath,
		MaxMessages: cfg.Devin.History.MaxMessages,
		MaxChars:    cfg.Devin.History.MaxChars,
		Recency:     cfg.Devin.History.Recency,
		SessionID:   sessionID,
	}).Retrieve(prompt, cwd)
}

// discoverDevinSessionID resolves the identity of the Devin session this hook
// process belongs to (Linux /proc ancestry walk; "" elsewhere or on failure).
func discoverDevinSessionID(dbPath string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if strings.TrimSpace(dbPath) == "" {
		dbPath = devinhistory.DefaultDBPath()
	}
	return devinsession.Discover("/proc", devinsession.DefaultLockDir(dbPath), os.Getpid())
}
