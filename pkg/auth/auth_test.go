package auth

import (
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	password := "securePassword123!"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	// Verify correct password
	valid, err := VerifyPassword(password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !valid {
		t.Error("Password should be valid")
	}

	// Verify wrong password
	valid, err = VerifyPassword("wrongPassword", hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed for wrong password: %v", err)
	}
	if valid {
		t.Error("Wrong password should not be valid")
	}
}
