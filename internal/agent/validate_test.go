package agent

import (
	"strings"
	"testing"
)

func TestValidateName_EmptyNameReturnsError(t *testing.T) {
	err := ValidateName("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("error should mention 'must not be empty', got: %v", err)
	}
}

func TestValidateName_ValidSimpleName(t *testing.T) {
	if err := ValidateName("finn"); err != nil {
		t.Fatalf("unexpected error for valid name: %v", err)
	}
}

func TestValidateName_ValidNameWithHyphen(t *testing.T) {
	if err := ValidateName("runner-1"); err != nil {
		t.Fatalf("unexpected error for valid name with hyphen: %v", err)
	}
}

func TestValidateName_ValidNameWithUnderscore(t *testing.T) {
	if err := ValidateName("my_agent"); err != nil {
		t.Fatalf("unexpected error for valid name with underscore: %v", err)
	}
}

func TestValidateName_ValidNameWithDigits(t *testing.T) {
	if err := ValidateName("agent42"); err != nil {
		t.Fatalf("unexpected error for valid name with digits: %v", err)
	}
}

func TestValidateName_TooLongReturnsError(t *testing.T) {
	long := strings.Repeat("a", 65)
	err := ValidateName(long)
	if err == nil {
		t.Fatal("expected error for name exceeding 64 chars")
	}
	if !strings.Contains(err.Error(), "64 characters") {
		t.Errorf("error should mention '64 characters', got: %v", err)
	}
}

func TestValidateName_MaxLengthAccepted(t *testing.T) {
	name := strings.Repeat("a", 64)
	if err := ValidateName(name); err != nil {
		t.Fatalf("unexpected error for 64-char name: %v", err)
	}
}

func TestValidateName_StartsWithDotReturnsError(t *testing.T) {
	err := ValidateName(".hidden")
	if err == nil {
		t.Fatal("expected error for name starting with dot")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error should mention 'invalid agent name', got: %v", err)
	}
}

func TestValidateName_StartsWithHyphenReturnsError(t *testing.T) {
	err := ValidateName("-bad")
	if err == nil {
		t.Fatal("expected error for name starting with hyphen")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error should mention 'invalid agent name', got: %v", err)
	}
}

func TestValidateName_DotDotReturnsError(t *testing.T) {
	err := ValidateName("..")
	if err == nil {
		t.Fatal("expected error for '..' name")
	}
}

func TestValidateName_SingleDotReturnsError(t *testing.T) {
	err := ValidateName(".")
	if err == nil {
		t.Fatal("expected error for '.' name")
	}
}

func TestValidateName_ContainsSlashReturnsError(t *testing.T) {
	err := ValidateName("../etc/passwd")
	if err == nil {
		t.Fatal("expected error for name containing slash")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error should mention 'invalid agent name', got: %v", err)
	}
}

func TestValidateName_ContainsBackslashReturnsError(t *testing.T) {
	err := ValidateName(`a\b`)
	if err == nil {
		t.Fatal("expected error for name containing backslash")
	}
}

func TestValidateName_ContainsSpaceReturnsError(t *testing.T) {
	err := ValidateName("a b")
	if err == nil {
		t.Fatal("expected error for name containing space")
	}
}

func TestValidateName_ContainsSpecialCharsReturnsError(t *testing.T) {
	for _, name := range []string{"a@b", "foo!bar", "x=y", "a;b", "a&b"} {
		err := ValidateName(name)
		if err == nil {
			t.Errorf("expected error for name %q", name)
		}
	}
}

func TestValidateName_AllPoolNamesAreValid(t *testing.T) {
	for _, name := range NamePool {
		if err := ValidateName(name); err != nil {
			t.Errorf("pool name %q should be valid, got: %v", name, err)
		}
	}
}

func TestValidateName_FallbackNamesAreValid(t *testing.T) {
	for _, prefix := range FallbackPrefix {
		name := prefix + "-1"
		if err := ValidateName(name); err != nil {
			t.Errorf("fallback name %q should be valid, got: %v", name, err)
		}
	}
}
