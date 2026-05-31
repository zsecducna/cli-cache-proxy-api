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
		method, idc, region, username, err := promptKiroMethod(scriptedPrompt(t, []string{""}, nil), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "builder-id" || idc != "" || username != "" {
			t.Fatalf("got method=%q idc=%q username=%q, want builder-id with empty idc/username", method, idc, username)
		}
		_ = region
	})

	t.Run("choice 1 selects builder-id", func(t *testing.T) {
		method, idc, _, _, err := promptKiroMethod(scriptedPrompt(t, []string{"1"}, nil), "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "builder-id" || idc != "" {
			t.Fatalf("got method=%q idc=%q, want builder-id", method, idc)
		}
	})

	t.Run("choice 2 collects idc start url, region, and username", func(t *testing.T) {
		method, idc, region, username, err := promptKiroMethod(
			scriptedPrompt(t, []string{"2", "https://d-90660ceab3.awsapps.com/start", "eu-west-1", "wqh9niny3"}, nil), "")
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
		if username != "wqh9niny3" {
			t.Fatalf("got username=%q, want wqh9niny3", username)
		}
	})

	t.Run("idc empty region falls back to default param", func(t *testing.T) {
		method, _, region, username, err := promptKiroMethod(
			scriptedPrompt(t, []string{"2", "https://x.awsapps.com/start", "", "bob"}, nil), "ap-southeast-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if method != "idc" || region != "ap-southeast-1" || username != "bob" {
			t.Fatalf("got method=%q region=%q username=%q, want idc/ap-southeast-1/bob", method, region, username)
		}
	})

	t.Run("idc empty start url errors", func(t *testing.T) {
		_, _, _, _, err := promptKiroMethod(scriptedPrompt(t, []string{"2", ""}, nil), "")
		if err == nil {
			t.Fatal("expected error for empty IDC start URL, got nil")
		}
	})

	t.Run("idc empty username errors", func(t *testing.T) {
		_, _, _, _, err := promptKiroMethod(
			scriptedPrompt(t, []string{"2", "https://d-90660ceab3.awsapps.com/start", "us-east-1", ""}, nil), "")
		if err == nil {
			t.Fatal("expected error for empty IDC username, got nil")
		}
	})
}
