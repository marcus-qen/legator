/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package security

import (
	"strings"
	"testing"
)

func TestSanitize_BearerToken(t *testing.T) {
	input := `Authorization: Bearer eyJhbGciOiJSUzI1NiIsImtpZCI6IkRFIn0.eyJpc3MiOiJrdWJlcm5ldGVzIn0.signature`
	result := Sanitize(input)
	if strings.Contains(result, "eyJ") {
		t.Errorf("JWT not sanitized: %s", result)
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output: %s", result)
	}
}

func TestSanitize_VaultToken(t *testing.T) {
	input := `vault token is hvs.CAESIFhBcmFuZG9tVGVzdFRva2Vu`
	result := Sanitize(input)
	if strings.Contains(result, "CAESIFhB") {
		t.Errorf("Vault token not sanitized: %s", result)
	}
}

func TestSanitize_AWSKeys(t *testing.T) {
	input := `AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`
	result := Sanitize(input)
	if strings.Contains(result, "wJalr") {
		t.Errorf("AWS secret not sanitized: %s", result)
	}

	input2 := `access key: AKIAIOSFODNN7EXAMPLE`
	result2 := Sanitize(input2)
	if strings.Contains(result2, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS access key not sanitized: %s", result2)
	}
}

func TestSanitize_PrivateKey(t *testing.T) {
	input := `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/yGWNseitguBx+w==
-----END RSA PRIVATE KEY-----`
	result := Sanitize(input)
	if strings.Contains(result, "MIIEpAI") {
		t.Errorf("private key not sanitized: %s", result)
	}
}

func TestSanitize_PasswordField(t *testing.T) {
	input := `password: super-secret-123!`
	result := Sanitize(input)
	if strings.Contains(result, "super-secret") {
		t.Errorf("password not sanitized: %s", result)
	}
}

func TestSanitize_APIKey(t *testing.T) {
	input := `api_key=sk-proj-1234567890abcdefghijklmnop`
	result := Sanitize(input)
	if strings.Contains(result, "1234567890") {
		t.Errorf("API key not sanitized: %s", result)
	}
}

func TestSanitize_KubeconfigData(t *testing.T) {
	input := `client-certificate-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUN5RENDQWJDZ0F3SUJBZ0lCQURBTkJna3Foa2lHOXcw`
	result := Sanitize(input)
	if strings.Contains(result, "LS0tLS1CRUdJT") {
		t.Errorf("kubeconfig cert data not sanitized: %s", result)
	}
}

func TestSanitize_PreservesNormalText(t *testing.T) {
	input := `pod nginx-abc123 is Running in namespace default. CPU usage: 50m. Memory: 128Mi.`
	result := Sanitize(input)
	if result != input {
		t.Errorf("normal text was modified: %q → %q", input, result)
	}
}

func TestSanitize_MixedContent(t *testing.T) {
	input := `Pod status: Running
Token: eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJrOHMifQ.sig123
Replicas: 3`
	result := Sanitize(input)
	if !strings.Contains(result, "Pod status: Running") {
		t.Error("normal content modified")
	}
	if !strings.Contains(result, "Replicas: 3") {
		t.Error("normal content modified")
	}
	if strings.Contains(result, "eyJhbGci") {
		t.Error("JWT not sanitized in mixed content")
	}
}

func TestContainsSecret(t *testing.T) {
	tests := []struct {
		text     string
		expected bool
	}{
		{"just normal text", false},
		{"Bearer eyJhbGciOiJSUzI1NiJ9.eyJ.sig", true},
		{"hvs.CAESIFhBcmFuZG9tVGVzdFRva2Vu", true},
		{"AKIAIOSFODNN7EXAMPLE", true},
		{"password: foo", true},
		{"pod is running", false},
	}

	for _, tt := range tests {
		got := ContainsSecret(tt.text)
		if got != tt.expected {
			t.Errorf("ContainsSecret(%q) = %v, want %v", tt.text, got, tt.expected)
		}
	}
}

func TestSanitizeActionResult_Truncation(t *testing.T) {
	input := "some normal text that is longer than the limit"
	result := SanitizeActionResult(input, 20)
	if len(result) > 40 { // 20 + "... (truncated)"
		t.Errorf("result too long: %d chars", len(result))
	}
	if !strings.Contains(result, "(truncated)") {
		t.Error("expected truncation marker")
	}
}

func TestSanitizeActionResult_NoTruncation(t *testing.T) {
	input := "short"
	result := SanitizeActionResult(input, 100)
	if result != "short" {
		t.Errorf("expected %q, got %q", input, result)
	}
}

func TestSanitizeMap(t *testing.T) {
	m := map[string]string{
		"endpoint":     "https://api.example.com",
		"api_token":    "secret-value-123",
		"namespace":    "default",
		"password":     "hunter2",
		"normal_field": "Bearer eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJrOHMifQ.sig123",
	}

	result := SanitizeMap(m)

	if result["endpoint"] != "https://api.example.com" {
		t.Errorf("endpoint modified: %s", result["endpoint"])
	}
	if result["api_token"] != "[REDACTED]" {
		t.Errorf("api_token not redacted: %s", result["api_token"])
	}
	if result["namespace"] != "default" {
		t.Errorf("namespace modified: %s", result["namespace"])
	}
	if result["password"] != "[REDACTED]" {
		t.Errorf("password not redacted: %s", result["password"])
	}
	if strings.Contains(result["normal_field"], "eyJhbG") {
		t.Error("JWT in normal_field not sanitized")
	}
}

func TestIsCredentialKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"password", true},
		{"PASSWORD", true},
		{"db_password", true},
		{"api_key", true},
		{"apiKey", true},
		{"secret", true},
		{"AWS_SECRET_ACCESS_KEY", true},
		{"token", true},
		{"private_key", true},
		{"endpoint", false},
		{"namespace", false},
		{"name", false},
	}

	for _, tt := range tests {
		got := isCredentialKey(tt.key)
		if got != tt.expected {
			t.Errorf("isCredentialKey(%q) = %v, want %v", tt.key, got, tt.expected)
		}
	}
}
