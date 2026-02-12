package main

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"

	"github.com/charmbracelet/ssh"
	"github.com/pquerna/otp/totp"
)

const (
	totpVerifiedKey = "totp_verified"
	totpRequiredKey = "totp_required"
)

// GenerateTOTPSecret generates a new random TOTP secret.
func GenerateTOTPSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("failed to generate TOTP secret: %w", err)
	}
	return base32.StdEncoding.EncodeToString(secret), nil
}

// ValidateTOTP validates a TOTP code against a secret.
func ValidateTOTP(secret string, code string) bool {
	if secret == "" || code == "" {
		return false
	}
	// Allow some time drift (1 step before and after)
	return totp.Validate(code, secret)
}

// GetTOTPVerified returns whether TOTP has been verified for this session.
func GetTOTPVerified(ctx ssh.Context) bool {
	if val := ctx.Value(totpVerifiedKey); val != nil {
		return val.(bool)
	}
	return false
}

// SetTOTPVerified sets the TOTP verified status for this session.
func SetTOTPVerified(ctx ssh.Context, verified bool) {
	ctx.SetValue(totpVerifiedKey, verified)
}

// GetTOTPRequired returns whether TOTP is required for this user.
func GetTOTPRequired(ctx ssh.Context) bool {
	if val := ctx.Value(totpRequiredKey); val != nil {
		return val.(bool)
	}
	return false
}

// SetTOTPRequired sets whether TOTP is required for this session.
func SetTOTPRequired(ctx ssh.Context, required bool) {
	ctx.SetValue(totpRequiredKey, required)
}

// NeedsTOTPVerification checks if the user has TOTP configured and hasn't verified yet.
func NeedsTOTPVerification(ctx ssh.Context, perms *Permission) bool {
	if perms == nil || perms.TOTPSecret == "" {
		return false
	}
	return !GetTOTPVerified(ctx)
}

// NeedsTOTPVerificationWithResolver checks if TOTP is needed using both context and resolver.
func NeedsTOTPVerificationWithResolver(ctx ssh.Context, resolver *TargetResolver, perms *Permission) bool {
	if perms == nil || perms.TOTPSecret == "" {
		return false
	}
	if GetTOTPVerified(ctx) {
		return false
	}
	if resolver != nil {
		resolver.mu.RLock()
		verified := resolver.TOTPVerified
		resolver.mu.RUnlock()
		if verified {
			return false
		}
	}
	return true
}

// GenerateTOTPKeyURL generates a provisioning URL for QR code generation.
func GenerateTOTPKeyURL(username string, secret string, issuer string) string {
	// This returns a URL that can be used to generate a QR code
	// Format: otpauth://totp/Issuer:username?secret=SECRET&issuer=Issuer
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s",
		issuer, username, secret, issuer)
}
