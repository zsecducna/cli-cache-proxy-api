package cmd

import "testing"

// scriptedPrompt returns a promptFn that yields the given answers in order, recording the
// prompt messages it was asked. It fails the test if more answers are requested than given.
func scriptedPrompt(t *testing.T, answers []string, asked *[]string) func(string) (string, error) {
	t.Helper()
	idx := 0
	return func(msg string) (string, error) {
		if asked != nil {
			*asked = append(*asked, msg)
		}
		if idx >= len(answers) {
			t.Fatalf("unexpected extra prompt: %q", msg)
		}
		ans := answers[idx]
		idx++
		return ans, nil
	}
}

// TestPromptKiroMethod verifies interactive method selection and IDC input collection.
func TestPromptKiroMethod(t *testing.T) {
	t.Run("default empty selects builder-id", func(t *testing.T) {
		method, idc, region, err := promptKiroMethod(scriptedPrompt(t, []string{""}, nil), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "builder-id" || idc != "" {
			t.Fatalf("got method=%q idc=%q, want builder-id with empty idc", method, idc)
		}
		_ = region
	})

	t.Run("choice 1 selects builder-id", func(t *testing.T) {
		method, idc, _, err := promptKiroMethod(scriptedPrompt(t, []string{"1"}, nil), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "builder-id" || idc != "" {
			t.Fatalf("got method=%q idc=%q, want builder-id", method, idc)
		}
	})

	t.Run("choice 2 collects idc start url and region", func(t *testing.T) {
		method, idc, region, err := promptKiroMethod(
			scriptedPrompt(t, []string{"2", "https://d-90660ceab3.awsapps.com/start", "eu-west-1"}, nil), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "idc" {
			t.Fatalf("got method=%q, want idc", method)
		}
		if idc != "https://d-90660ceab3.awsapps.com/start" {
			t.Fatalf("got idc=%q", idc)
		}
		if region != "eu-west-1" {
			t.Fatalf("got region=%q, want eu-west-1", region)
		}
	})

	t.Run("idc empty region falls back to default param", func(t *testing.T) {
		method, _, region, err := promptKiroMethod(
			scriptedPrompt(t, []string{"2", "https://x.awsapps.com/start", ""}, nil), "ap-southeast-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "idc" || region != "ap-southeast-1" {
			t.Fatalf("got method=%q region=%q, want idc/ap-southeast-1", method, region)
		}
	})

	t.Run("idc empty start url errors", func(t *testing.T) {
		_, _, _, err := promptKiroMethod(scriptedPrompt(t, []string{"2", ""}, nil), "")
		if err == nil {
			t.Fatal("expected error for empty IDC start URL, got nil")
		}
	})
}
