package enhancer

import (
	"context"
	"log/slog"
	"strings"
	"unicode"

	"github.com/abadojack/whatlanggo"
)

// scriptClass is a coarse, short-text-robust classification of the dominant
// writing system of a string. openPE's language-preservation guard compares the
// class of the user's original prompt against the class of the enhanced output;
// a difference is a language drift we can act on reliably (e.g. English↔Chinese,
// which is a Latin↔Han cross-script difference). Same-class drift within one
// script (e.g. English↔French, both Latin) is intentionally NOT flagged — it is
// rare, hard to detect on short text, and the user asked not to over-handle such
// edges (fail-open).
type scriptClass int

const (
	scriptUnknown scriptClass = iota // too short / no dominant script → guard no-op
	scriptLatin
	scriptChinese
	scriptJapanese
	scriptKorean
	scriptOther // a detected non-Latin/CJK script (Cyrillic, Arabic, ...)
)

func (c scriptClass) String() string {
	switch c {
	case scriptLatin:
		return "latin"
	case scriptChinese:
		return "chinese"
	case scriptJapanese:
		return "japanese"
	case scriptKorean:
		return "korean"
	case scriptOther:
		return "other"
	default:
		return "unknown"
	}
}

// classifyScript picks the dominant writing system by rune counts. Order matters:
// Hangul → Korean; Kana → Japanese (Japanese mixes Kana + Han, so Kana wins over
// Han); Han (no Kana/Hangul) → Chinese; otherwise Latin if there is enough Latin.
// The small thresholds keep a stray CJK character in an English sentence (e.g. a
// quoted identifier) from flipping the class, and reject too-short input.
func classifyScript(s string) scriptClass {
	var han, kana, hangul, latin, other int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			kana++
		case unicode.Is(unicode.Han, r):
			han++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			latin++
		case unicode.Is(unicode.Cyrillic, r) || unicode.Is(unicode.Arabic, r) || unicode.Is(unicode.Greek, r) || unicode.Is(unicode.Hebrew, r):
			other++
		}
	}
	switch {
	case hangul >= 2:
		return scriptKorean
	case kana >= 2:
		return scriptJapanese
	case han >= 2:
		return scriptChinese
	case latin >= 6:
		return scriptLatin
	case other >= 4:
		return scriptOther
	default:
		return scriptUnknown
	}
}

// languageName returns a human language name for the re-anchor directive. The
// class fixes CJK names (whatlanggo's top guess is reliable there); for Latin we
// trust whatlanggo's top-guess language (correct for the dominant language even
// when its IsReliable() is conservative — e.g. English, French), falling back to
// English only if it yields nothing usable.
func languageName(s string, class scriptClass) string {
	switch class {
	case scriptChinese:
		return "Chinese (中文)"
	case scriptJapanese:
		return "Japanese (日本語)"
	case scriptKorean:
		return "Korean (한국어)"
	case scriptLatin, scriptOther:
		if name := strings.TrimSpace(whatlanggo.LangToString(whatlanggo.Detect(s).Lang)); name != "" {
			return name
		}
		return "English"
	default:
		return ""
	}
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
// and both the input and output languages were classifiable. It is the
// observability hook: wire WithLanguageGuardObserver to feed Prometheus
// counters, and/or a *slog.Logger for a structured record.
type LanguageGuardEvent struct {
	InputLang  string // script class of the user's ORIGINAL prompt (the source of truth)
	OutputLang string // script class of the first model output
	Mismatch   bool   // input and output classes differed
	Retried    bool   // a re-anchored retry was issued
	Corrected  bool   // the retry produced the input's language class
	FinalLang  string // script class of the returned text
}

// applyLanguageGuard is the post-processing step. It returns the (possibly
// re-anchored) enhanced prompt, plus an event describing what happened (nil when
// the guard is disabled or either side was unclassifiable). It never returns an
// error: a failed/rejected retry falls back to the original output so the guard
// can only improve or no-op, never break, a response.
//
// The target language is derived SOLELY from inputPrompt (the user's original
// current prompt). It is never taken from the model output or the conversation
// history, so the "correct" language cannot be polluted by what the model
// produced or by prior turns in another language.
func (s *Service) applyLanguageGuard(ctx context.Context, completion CompletionRequest, inputPrompt, enhanced string) (string, *LanguageGuardEvent) {
	if s.languageGuard == nil || !s.languageGuard.Enabled {
		return enhanced, nil
	}
	inClass := classifyScript(inputPrompt)
	outClass := classifyScript(enhanced)
	// Fail-open: if the user's prompt (or the output) is unclassifiable
	// (too short / mixed / no dominant script), do nothing. Never fall back to
	// the output's or history's language as the target.
	if inClass == scriptUnknown || outClass == scriptUnknown {
		return enhanced, nil
	}
	ev := &LanguageGuardEvent{InputLang: inClass.String(), OutputLang: outClass.String(), FinalLang: outClass.String()}
	if inClass == outClass {
		return enhanced, ev // consistent: the common case, no cost
	}
	ev.Mismatch = true
	if !s.languageGuard.Reanchor {
		return enhanced, ev // detect-only mode
	}
	ev.Retried = true
	retry := completion
	retry.System = completion.System + languageDirective(languageName(inputPrompt, inClass))
	out, err := s.provider.Complete(ctx, retry)
	if err != nil {
		return enhanced, ev // keep original on retry error
	}
	retried := strings.TrimSpace(out.Text)
	if retried == "" {
		return enhanced, ev
	}
	if classifyScript(retried) == inClass {
		ev.Corrected = true
		ev.FinalLang = inClass.String()
		return retried, ev
	}
	// Retry still not in the user's language: keep the original rather than swap
	// one wrong-language output for another. Caller warns.
	return enhanced, ev
}

// languageDirective is the strong, explicit re-anchor appended to the system
// prompt for the retry. Naming the concrete target language is far more forceful
// than the soft in-prompt rule that the first attempt already ignored.
func languageDirective(name string) string {
	return "\n\nCRITICAL LANGUAGE REQUIREMENT: the user's original request is written in " + name +
		". You MUST write the ENTIRE enhanced prompt in " + name +
		" — every sentence. Do not switch to any other language for any part of it."
}

// reportGuard emits a structured record and invokes the metrics observer. Both
// sinks are optional; either may be nil.
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
		slog.String("input_lang", ev.InputLang),
		slog.String("output_lang", ev.OutputLang),
		slog.Bool("retried", ev.Retried),
		slog.Bool("corrected", ev.Corrected),
		slog.String("final_lang", ev.FinalLang),
	)
}

// languageMismatchWarning is the user-facing warning appended to
// Response.Warnings when the guard could not deliver the output in the user's
// language.
func languageMismatchWarning(ev *LanguageGuardEvent) string {
	return "enhanced prompt language (" + ev.FinalLang +
		") does not match your request language (" + ev.InputLang +
		"); you may want to regenerate."
}
