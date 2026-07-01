package enhancer

import (
	"context"
	"log/slog"
	"strings"
	"unicode"
)

// Language is a coarse script classification. openPE's observed language-drift
// failure mode is strictly English<->Chinese (a model producing a Chinese
// enhanced prompt for an English request, or vice versa), so the guard only
// needs to tell CJK script apart from Latin script. Anything it cannot classify
// confidently is LangUnknown and never trips the guard (fail-open: ambiguous or
// mixed input is left exactly as the model produced it).
type Language int

const (
	LangUnknown Language = iota
	LangCJK              // Chinese / Japanese / Korean scripts (Chinese in practice)
	LangLatin            // English and other Latin-script languages
)

func (l Language) String() string {
	switch l {
	case LangCJK:
		return "cjk"
	case LangLatin:
		return "latin"
	default:
		return "unknown"
	}
}

// directiveName is the natural-language name used when re-anchoring the model.
// Empty for LangUnknown (which never reaches the re-anchor path).
func (l Language) directiveName() string {
	switch l {
	case LangCJK:
		return "Chinese (中文)"
	case LangLatin:
		return "English"
	default:
		return ""
	}
}

// Detection thresholds over script-bearing runes (CJK + Latin letters only;
// digits, punctuation, whitespace and symbols are ignored). Enhanced Chinese
// prompts routinely embed Latin code identifiers and paths, so "CJK" is declared
// well below 50%; English prompts are ~0% CJK. The gap in between is deliberately
// left LangUnknown so the guard never fires on genuinely mixed text.
const (
	cjkDominantRatio = 0.20 // >= this fraction of script runes are CJK -> LangCJK
	latinMaxCJKRatio = 0.05 // <= this fraction CJK (rest Latin)        -> LangLatin
	minScriptRunes   = 4    // fewer script runes than this -> too short to classify
)

// detectLanguage classifies s as CJK, Latin, or Unknown using the ratio of CJK
// runes to script-bearing runes. It is allocation-free and O(len(s)) — a few
// microseconds even for long prompts, well under the guard's latency budget.
func detectLanguage(s string) Language {
	var cjk, latin int
	for _, r := range s {
		switch {
		case isCJK(r):
			cjk++
		case isLatinLetter(r):
			latin++
		}
	}
	total := cjk + latin
	if total < minScriptRunes {
		return LangUnknown
	}
	ratio := float64(cjk) / float64(total)
	switch {
	case ratio >= cjkDominantRatio:
		return LangCJK
	case ratio <= latinMaxCJKRatio:
		return LangLatin
	default:
		return LangUnknown
	}
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func isLatinLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// LanguageGuardConfig configures the post-processing language-preservation
// guard. The zero value (Enabled=false) is a no-op, so a Service built without
// the guard behaves exactly as before it existed.
type LanguageGuardConfig struct {
	// Enabled turns the guard on.
	Enabled bool
	// Reanchor selects strategy 1 (re-request once with an explicit language
	// directive) on a detected mismatch. When false, the guard uses strategy 2
	// only (detect + warn, no extra model call), which adds zero latency.
	Reanchor bool
}

// LanguageGuardEvent is emitted once per enhancement when the guard is enabled
// and both input and output languages were classifiable. It is the observability
// hook: wire WithLanguageGuardObserver to feed Prometheus counters, and/or a
// *slog.Logger to get a structured record.
type LanguageGuardEvent struct {
	InputLang  Language
	OutputLang Language // language of the FIRST model output
	Mismatch   bool     // input and output languages differed
	Retried    bool     // a re-anchored retry was issued
	Corrected  bool     // the retry produced the input's language
	FinalLang  Language // language of the returned text
}

// applyLanguageGuard is the post-processing step. It returns the (possibly
// re-anchored) enhanced prompt, plus an event describing what happened (nil when
// the guard is disabled or either side was unclassifiable). It never returns an
// error: a failed/rejected retry falls back to the original output so the guard
// can only improve or no-op, never break, a response.
func (s *Service) applyLanguageGuard(ctx context.Context, completion CompletionRequest, inputPrompt, enhanced string) (string, *LanguageGuardEvent) {
	if s.languageGuard == nil || !s.languageGuard.Enabled {
		return enhanced, nil
	}
	inLang := detectLanguage(inputPrompt)
	outLang := detectLanguage(enhanced)
	// Only act when BOTH sides are confidently classified. Unknown on either
	// side (short/mixed/ambiguous) -> leave untouched.
	if inLang == LangUnknown || outLang == LangUnknown {
		return enhanced, nil
	}
	ev := &LanguageGuardEvent{InputLang: inLang, OutputLang: outLang, FinalLang: outLang}
	if inLang == outLang {
		return enhanced, ev // consistent: the common case, no cost
	}
	ev.Mismatch = true
	if !s.languageGuard.Reanchor {
		return enhanced, ev // detect-only mode
	}
	ev.Retried = true
	retry := completion
	retry.System = completion.System + languageDirective(inLang)
	out, err := s.provider.Complete(ctx, retry)
	if err != nil {
		return enhanced, ev // keep original on retry error
	}
	retried := strings.TrimSpace(out.Text)
	if retried == "" {
		return enhanced, ev
	}
	if detectLanguage(retried) == inLang {
		ev.Corrected = true
		ev.FinalLang = inLang
		return retried, ev
	}
	// Retry still not in the user's language: keep the original rather than
	// swap one wrong-language output for another. Caller warns.
	return enhanced, ev
}

// languageDirective is the strong, explicit re-anchor appended to the system
// prompt for the retry. Naming the concrete target language is far more forceful
// than the soft in-prompt rule that the first attempt already ignored.
func languageDirective(target Language) string {
	name := target.directiveName()
	return "\n\nCRITICAL LANGUAGE REQUIREMENT: the user's original request is written in " + name +
		". You MUST write the ENTIRE enhanced prompt in " + name +
		" — every sentence. Do not switch to any other language for any part of it."
}

// logLanguageGuard emits a structured record and invokes the metrics observer.
// Both sinks are optional; either may be nil.
func (s *Service) reportGuard(ev *LanguageGuardEvent) {
	if ev == nil {
		return
	}
	if s.guardObserver != nil {
		s.guardObserver(*ev)
	}
	if s.logger == nil || !ev.Mismatch {
		return
	}
	level := slog.LevelWarn
	msg := "language guard: output language did not match input"
	if ev.Corrected {
		level = slog.LevelInfo
		msg = "language guard: corrected output language via re-anchor retry"
	}
	s.logger.LogAttrs(context.Background(), level, msg,
		slog.String("input_lang", ev.InputLang.String()),
		slog.String("output_lang", ev.OutputLang.String()),
		slog.Bool("retried", ev.Retried),
		slog.Bool("corrected", ev.Corrected),
		slog.String("final_lang", ev.FinalLang.String()),
	)
}

// languageMismatchWarning is the user-facing warning appended to Response.Warnings
// when the guard could not deliver the output in the user's language.
func languageMismatchWarning(ev *LanguageGuardEvent) string {
	return "enhanced prompt language (" + ev.FinalLang.String() +
		") does not match your request language (" + ev.InputLang.String() +
		"); you may want to regenerate."
}
