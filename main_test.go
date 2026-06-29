package main

import (
	"os"
	"testing"
)

func TestIsValidBirthdayFormat(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"06-28", true},
		{"01-01", true},
		{"12-31", true},
		{"2-29", false}, // must be 02-29
		{"02-29", true},
		{"06/28", false},
		{"1995-06-28", false},
		{"", false},
		{"abcd", false},
	}

	for _, test := range tests {
		result := isValidBirthdayFormat(test.input)
		if result != test.expected {
			t.Errorf("isValidBirthdayFormat(%q) = %v; expected %v", test.input, result, test.expected)
		}
	}
}

func TestReadBirthdays(t *testing.T) {
	records, err := readBirthdays("birthdays.xlsx")
	if err != nil {
		t.Fatalf("Expected no error reading excel, got %v", err)
	}

	// From birthdays.xlsx:
	// Total valid should be 3: 李四, 張三, 王五
	if len(records) != 3 {
		t.Errorf("Expected 3 valid records, got %d", len(records))
		for i, r := range records {
			t.Logf("Record %d: Name=%s, Birthday=%s", i, r.Name, r.Birthday)
		}
	}

	hasLi := false
	hasChang := false
	hasWang := false
	for _, r := range records {
		if r.Name == "李四" {
			hasLi = true
		}
		if r.Name == "張三" {
			hasChang = true
		}
		if r.Name == "王五" {
			hasWang = true
		}
	}

	if !hasLi {
		t.Error("Missing expected record '李四'")
	}
	if !hasChang {
		t.Error("Missing expected record '張三'")
	}
	if !hasWang {
		t.Error("Missing expected record '王五'")
	}
}

func TestRuntimeConfig(t *testing.T) {
	// Set path to temporary test config file
	originalPath := runtimeConfigPath
	runtimeConfigPath = "test_config.json"
	defer func() {
		os.Remove(runtimeConfigPath)
		runtimeConfigPath = originalPath
	}()

	// Load config with fallback group
	loadRuntimeConfig("test-fallback-group")

	if getGroupLineID() != "test-fallback-group" {
		t.Errorf("Expected fallback group 'test-fallback-group', got %q", getGroupLineID())
	}

	if getGreetingTemplate() != "祝{day}生日的 {names} 生日快樂！🎉🎂" {
		t.Errorf("Expected default greeting template, got %q", getGreetingTemplate())
	}

	// Modify settings
	err := setGroupLineID("new-group-id")
	if err != nil {
		t.Fatalf("Failed to set group ID: %v", err)
	}

	err = setGreetingTemplate("祝 {names} 天天開心！")
	if err != nil {
		t.Fatalf("Failed to set greeting template: %v", err)
	}

	// Re-load config to test persistence
	loadRuntimeConfig("ignored-fallback")

	if getGroupLineID() != "new-group-id" {
		t.Errorf("Expected persistent group 'new-group-id', got %q", getGroupLineID())
	}

	if getGreetingTemplate() != "祝 {names} 天天開心！" {
		t.Errorf("Expected persistent template, got %q", getGreetingTemplate())
	}
}

func TestHandleAdminCommand(t *testing.T) {
	// Setup test configuration path
	originalPath := runtimeConfigPath
	runtimeConfigPath = "test_config_cmd.json"
	defer func() {
		os.Remove(runtimeConfigPath)
		runtimeConfigPath = originalPath
	}()

	// Load dynamic config
	loadRuntimeConfig("initial-group-id")

	// Test command execution
	// 1. /help (no crash, prints log in test mode)
	handleAdminCommand(nil, "reply-token", "/help")

	// 2. /show (no crash)
	handleAdminCommand(nil, "reply-token", "/show")

	// 3. /setgroup
	handleAdminCommand(nil, "reply-token", "/setgroup C12345678901234567890123456789012")
	if getGroupLineID() != "C12345678901234567890123456789012" {
		t.Errorf("Expected group ID to be updated to C12345678901234567890123456789012, got %q", getGroupLineID())
	}

	// 4. /setgroup invalid format
	handleAdminCommand(nil, "reply-token", "/setgroup invalid")
	if getGroupLineID() == "invalid" {
		t.Error("Group ID should not be set to an invalid format")
	}

	// 5. /settemplate
	handleAdminCommand(nil, "reply-token", "/settemplate 祝 {names} 生日大吉！")
	if getGreetingTemplate() != "祝 {names} 生日大吉！" {
		t.Errorf("Expected template to be updated to '祝 {names} 生日大吉！', got %q", getGreetingTemplate())
	}

	// 6. Unknown command
	handleAdminCommand(nil, "reply-token", "/unknown")
}
