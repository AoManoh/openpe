package enhancer

import (
	"fmt"
	"regexp"
	"strings"
)

// Content warnings are the deterministic, model-independent last line of
// defense behind the prompt guardrails (v7g/v7h): the guardrails lower the
// incidence of fabricated specifics, but they are probabilistic. Two real
// incidents (2026-07-02) motivated machine-checkable detection on the
// enhancer's OUTPUT: invented numbers ("批A 52 请求" — no such figure anywhere
// in the context) and fabricated irreversible actions (an enhanced prompt
// ordering a push the user never decided; it got executed). Detection only
// WARNS — openPE never rewrites or blocks the enhanced prompt; the warning
// rides Response.Warnings into the hook disclosure so the user sees it before
// pasting (review mode) or in the injected header (inject mode).
//
// Rules are intentionally lexical (substring / word-boundary, no NLP): fully
// deterministic, explainable, <1ms, zero dependencies. They over-report by
// design ("宁可多报") — the cost of a false alarm is one advisory line.
//
// Requirements: docs/requirements/2026-07-02-enhancement-warnings.md.

// ContentWarningsConfig controls the output-side warning checks. The zero
// value disables them; config.Load wires the OPENPE_WARNINGS_* env vars.
type ContentWarningsConfig struct {
	Enabled bool
	// ExtraActions extends the irreversible-action word list (each entry is
	// its own synonym group; matched like the built-ins).
	ExtraActions []string
	// NumMaxLen is the maximum digit-run length checked by the number rule;
	// longer runs are ids/hashes/timestamps, not quantities. Default 5.
	NumMaxLen int
}

// actionGroup is one irreversible-action synonym family (W2). Triggers (what
// fires on the ENHANCED text) are deliberately precise — directional /
// imperative forms only — because domain vocabulary is full of benign
// homonyms (消息"推送"机制、重构中"移除"轮询、"如何部署"说明). Mentions (what
// the USER'S OWN prompt may say to whitelist the group) are deliberately
// loose: any way the user names the action counts as their decision.
// History is NOT consulted for whitelisting — incident #3 fabricated its push
// order precisely from a history status line ("13 commits not pushed yet").
// Trigger entries may be prefixed "re:" for a regex (used where zh needs an
// object window, e.g. 删除…表/库/数据).
type actionGroup struct {
	name     string
	triggers []string
	mentions []string
}

var actionGroups = []actionGroup{
	{"push", []string{"push", "推送到", "推送至"}, []string{"push", "推送", "推上去", "推吧"}},
	{"deploy", []string{"deploy", "部署到", "上线到"}, []string{"deploy", "ship", "部署", "上线"}},
	{"delete", []string{"delete", "drop", `re:(删除|删掉|移除)[^。，,\n]{0,8}(表|库|数据|文件|分支|远程|生产)`}, []string{"delete", "drop", "删除", "删掉", "移除", "删"}},
	{"publish", []string{"publish", "发布到", "正式发布"}, []string{"publish", "发布"}},
	{"pay", []string{"pay", "支付", "付款"}, []string{"pay", "支付", "付款", "付"}},
	{"force", []string{"force-push", "force push", "强推", "强制提交"}, []string{"force", "强推", "强制"}},
	{"rm", []string{"rm -rf", "rm "}, []string{"rm", "删除", "删"}},
	{"reset", []string{"reset --hard"}, []string{"reset"}},
}

// detectContentWarnings runs W1 (out-of-context numbers) and W2 (undecided
// irreversible actions) against the enhanced text and returns advisory lines
// in the user's language. It never errors and never mutates the enhancement.
func detectContentWarnings(req Request, enhanced string, cfg ContentWarningsConfig, language string) []string {
	if !cfg.Enabled || strings.TrimSpace(enhanced) == "" {
		return nil
	}
	var warnings []string
	if fab := outOfContextNumbers(req, enhanced, cfg.numMaxLen()); len(fab) > 0 {
		warnings = append(warnings, numbersWarning(fab, language))
	}
	if acts := undecidedActions(req.Prompt, enhanced, cfg.ExtraActions); len(acts) > 0 {
		warnings = append(warnings, actionsWarning(acts, language))
	}
	return warnings
}

func (c ContentWarningsConfig) numMaxLen() int {
	if c.NumMaxLen <= 0 {
		return 5
	}
	return c.NumMaxLen
}

// listMarkerRE strips ordered-list numbering ("1. " / "2、" / "3) ") before
// number extraction: enumeration formatting is not a fabricated quantity.
// (Learned from the eval checker's first false-positive wave.)
var listMarkerRE = regexp.MustCompile(`(?m)^\s{0,8}\d{1,2}\s*[.、)）]`)

// outOfContextNumbers returns digit runs present in the enhanced text but
// absent from everything the model was given (prompt, history, rules,
// guidelines, retrieval, context files). Digit runs embedded in identifiers
// (letter/underscore-adjacent, e.g. "v7g", "P95") are skipped on both sides.
func outOfContextNumbers(req Request, enhanced string, maxLen int) []string {
	// 0 and 1 are ubiquitous in technical prose (edge cases, exit codes,
	// booleans) and carry no fabrication signal — always allowed.
	allowed := map[string]bool{"0": true, "1": true}
	collect := func(s string) {
		for _, n := range standaloneNumbers(s, 0) {
			allowed[n] = true
		}
	}
	collect(req.Prompt)
	for _, m := range req.History {
		collect(m.Content)
	}
	for _, r := range req.Rules {
		collect(r)
	}
	for _, g := range req.Guidelines {
		collect(g)
	}
	for _, r := range req.Context.Retrieval {
		collect(r)
	}
	for _, f := range req.Context.Files {
		collect(f.Path)
		collect(f.Content)
	}

	var fab []string
	seen := map[string]bool{}
	for _, n := range standaloneNumbers(listMarkerRE.ReplaceAllString(enhanced, ""), maxLen) {
		if !allowed[n] && !seen[n] {
			seen[n] = true
			fab = append(fab, n)
		}
	}
	return fab
}

// standaloneNumbers extracts digit runs not embedded in identifiers. maxLen 0
// means no length cap (used for the allowed set, so long input ids still
// whitelist their shorter embedded forms is NOT done — symmetry keeps the
// comparison honest).
func standaloneNumbers(s string, maxLen int) []string {
	isWord := func(b byte) bool {
		return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
	}
	var out []string
	for i := 0; i < len(s); {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		letterBefore := i > 0 && isWord(s[i-1])
		letterAfter := j < len(s) && isWord(s[j])
		if !letterBefore && !letterAfter && (maxLen <= 0 || j-i <= maxLen) {
			out = append(out, s[i:j])
		}
		i = j
	}
	return out
}

// undecidedActions returns the action words present in the enhanced text whose
// synonym group has NO mention in the user's own prompt.
func undecidedActions(prompt string, enhanced string, extra []string) []string {
	groups := actionGroups
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if e != "" {
			groups = append(groups, actionGroup{name: e, triggers: []string{e}, mentions: []string{e}})
		}
	}
	var hits []string
	for _, group := range groups {
		matched := ""
		for _, w := range group.triggers {
			if containsAction(enhanced, w) {
				matched = strings.TrimPrefix(w, "re:")
				if strings.HasPrefix(w, "re:") {
					matched = group.name
				}
				break
			}
		}
		if matched == "" {
			continue
		}
		mentioned := false
		for _, w := range group.mentions {
			if containsAction(prompt, w) {
				mentioned = true
				break
			}
		}
		if !mentioned {
			hits = append(hits, matched)
		}
	}
	return hits
}

// containsAction matches "re:"-prefixed entries as regexes, ASCII words
// case-insensitively on word boundaries (so "push" does not match "pushed",
// and "rm " does not match "form"), and non-ASCII (Chinese) words by plain
// substring.
func containsAction(text string, word string) bool {
	if word == "" {
		return false
	}
	if pat, ok := strings.CutPrefix(word, "re:"); ok {
		re, err := regexp.Compile(pat)
		return err == nil && re.MatchString(text)
	}
	if isASCII(word) {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(strings.TrimSpace(word)) + `\b`)
		return re.MatchString(text)
	}
	return strings.Contains(text, word)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func numbersWarning(fab []string, language string) string {
	list := strings.Join(fab, ", ")
	if isEnglish(language) {
		return fmt.Sprintf("openPE warning: the enhanced prompt contains numbers not present in your input or context [%s] — please verify they are not fabricated.", list)
	}
	return fmt.Sprintf("openPE 提醒：增强结果包含上下文中未出现的数字 [%s]，请核对是否臆造。", list)
}

func actionsWarning(acts []string, language string) string {
	list := strings.Join(acts, ", ")
	if isEnglish(language) {
		return fmt.Sprintf("openPE warning: the enhanced prompt contains action(s) your original input did not request [%s] — if you did not decide this, remove it before sending.", list)
	}
	return fmt.Sprintf("openPE 提醒：增强结果包含你的原始输入中没有的动作「%s」，若非你的决策请删除后再发送。", list)
}

func isEnglish(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}
