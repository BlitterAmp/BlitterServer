package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordProducesPHCString(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("want PHC argon2id string, got %q", h)
	}
}

func TestVerifyPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("hunter2hunter2", h)
	if err != nil || !ok {
		t.Fatalf("correct password must verify: %v %v", ok, err)
	}
	ok, err = VerifyPassword("wrong password", h)
	if err != nil || ok {
		t.Fatalf("wrong password must not verify: %v %v", ok, err)
	}
}

func TestHashPasswordSaltsAreUnique(t *testing.T) {
	a, _ := HashPassword("same input")
	b, _ := HashPassword("same input")
	if a == b {
		t.Fatal("two hashes of the same input must differ (random salt)")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "argon2id", "$argon2id$v=19$m=65536,t=1,p=4$notb64!$x", "$bcrypt$whatever"} {
		if _, err := VerifyPassword("x", h); err == nil {
			t.Errorf("malformed hash %q must error", h)
		}
	}
}
