package ui

import (
	"testing"

	"github.com/ThiraSoft/neonmux/internal/prompt"
)

func TestConfigPrompterDefaults(t *testing.T) {
	c, _, err := ParseConfig([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.PrompterWPM != 300 || c.PrompterContext != 3 || c.PrompterIntermediate {
		t.Fatalf("defaults %d %d %v", c.PrompterWPM, c.PrompterContext, c.PrompterIntermediate)
	}
	if want := (prompt.Opts{SkipCode: true, ShortPaths: true, StripMarkdown: true, Pauses: true}); c.PromptOpts() != want {
		t.Fatalf("opts %+v", c.PromptOpts())
	}
}

func TestConfigPrompterClamp(t *testing.T) {
	c, _, err := ParseConfig([]byte(`{"prompter_wpm": 5, "prompter_context": 40, "prompter_skip_code": false, "prompter_pauses": false}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.PrompterWPM != 100 || c.PrompterContext != 8 {
		t.Fatalf("clamped %d %d", c.PrompterWPM, c.PrompterContext)
	}
	if o := c.PromptOpts(); o.SkipCode || o.Pauses || !o.ShortPaths {
		t.Fatalf("opts %+v", o)
	}
	c, _, _ = ParseConfig([]byte(`{"prompter_wpm": 5000}`))
	if c.PrompterWPM != 1000 {
		t.Fatalf("wpm %d", c.PrompterWPM)
	}
}
