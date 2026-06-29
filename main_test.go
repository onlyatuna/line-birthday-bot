package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
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

	if getReviewTemplate() != "{day} ({date}) 生日人員：{names}\n請審查是否在群組發布祝賀訊息。" {
		t.Errorf("Expected default review template, got %q", getReviewTemplate())
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

	err = setReviewTemplate("審查生日：{day} 生日壽星 {names}")
	if err != nil {
		t.Fatalf("Failed to set review template: %v", err)
	}

	// Re-load config to test persistence
	loadRuntimeConfig("ignored-fallback")

	if getGroupLineID() != "new-group-id" {
		t.Errorf("Expected persistent group 'new-group-id', got %q", getGroupLineID())
	}

	if getGreetingTemplate() != "祝 {names} 天天開心！" {
		t.Errorf("Expected persistent template, got %q", getGreetingTemplate())
	}

	if getReviewTemplate() != "審查生日：{day} 生日壽星 {names}" {
		t.Errorf("Expected persistent review template, got %q", getReviewTemplate())
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

	// 6. /setreviewtemplate
	handleAdminCommand(nil, "reply-token", "/setreviewtemplate 審查通知：{day} ({date})")
	if getReviewTemplate() != "審查通知：{day} ({date})" {
		t.Errorf("Expected review template to be updated, got %q", getReviewTemplate())
	}

	// 7. /setreviewtemplate invalid empty
	handleAdminCommand(nil, "reply-token", "/setreviewtemplate")

	// 8. Unknown command
	handleAdminCommand(nil, "reply-token", "/unknown")
}

func TestCreateGreetingMessage(t *testing.T) {
	// Backup original templates
	originalGreetingTemplate := getGreetingTemplate()
	originalReviewTemplate := getReviewTemplate()
	defer func() {
		_ = setGreetingTemplate(originalGreetingTemplate)
		_ = setReviewTemplate(originalReviewTemplate)
	}()

	_ = setGreetingTemplate("祝{day}生日的 {names} 生日快樂！🎉🎂")

	records := []BirthdayRecord{
		{Name: "張三", Birthday: "06-28", LineID: "U11111111111111111111111111111111"},
		{Name: "李四", Birthday: "06-28", LineID: ""}, // no LINE ID
		{Name: "王五", Birthday: "06-28", LineID: "U33333333333333333333333333333333"},
	}

	// 1. Test with mixed mentions
	msg := createGreetingMessage("today", []string{"張三", "李四", "王五"}, records)
	
	// Assert it is a CustomTextMessageV2
	v2, ok := msg.(*CustomTextMessageV2)
	if !ok {
		t.Fatalf("Expected CustomTextMessageV2 type, got %T", msg)
	}

	expectedText := "祝今天生日的 {user_0}、李四、{user_1} 生日快樂！🎉🎂"
	if v2.Text != expectedText {
		t.Errorf("Expected text %q, got %q", expectedText, v2.Text)
	}

	if len(v2.Substitution) != 2 {
		t.Errorf("Expected 2 substitutions, got %d", len(v2.Substitution))
	}

	sub0, ok := v2.Substitution["user_0"].(*messaging_api.MentionSubstitutionObject)
	if !ok {
		t.Fatalf("Expected MentionSubstitutionObject for user_0, got %T", v2.Substitution["user_0"])
	}
	if sub0.Type != "mention" {
		t.Errorf("Expected substitution type 'mention', got %q", sub0.Type)
	}
	target0, ok := sub0.Mentionee.(*messaging_api.UserMentionTarget)
	if !ok {
		t.Fatalf("Expected UserMentionTarget, got %T", sub0.Mentionee)
	}
	if target0.UserId != "U11111111111111111111111111111111" {
		t.Errorf("Expected UserId 'U11111111111111111111111111111111', got %q", target0.UserId)
	}

	// 2. Test with no mentions
	msgPlain := createGreetingMessage("today", []string{"李四"}, records)
	plain, ok := msgPlain.(*messaging_api.TextMessage)
	if !ok {
		t.Fatalf("Expected messaging_api.TextMessage type, got %T", msgPlain)
	}
	if plain.Text != "祝今天生日的 李四 生日快樂！🎉🎂" {
		t.Errorf("Expected text '祝今天生日的 李四 生日快樂！🎉🎂', got %q", plain.Text)
	}
}

func TestCollectGroupMember(t *testing.T) {
	originalPath := membersFilePath
	membersFilePath = "test_members.json"
	defer func() {
		os.Remove(membersFilePath)
		membersFilePath = originalPath
	}()

	// Verify empty list command output
	handleAdminCommand(nil, "reply-token", "/listmembers")

	// Pre-populate members.json
	members := map[string]string{
		"U11111111111111111111111111111111": "張三",
	}
	data, _ := json.Marshal(members)
	_ = os.WriteFile(membersFilePath, data, 0644)

	// Verify populated list command output
	handleAdminCommand(nil, "reply-token", "/listmembers")
}
