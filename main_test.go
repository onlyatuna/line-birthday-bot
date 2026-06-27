package main

import (
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
		t.Fatalf("Failed to read birthdays from excel: %v", err)
	}

	// From scratch data:
	// row 2: 李四 (today)
	// row 3: 張三 (tomorrow)
	// row 4: 王五 (10-10)
	// row 5: empty name (should be skipped)
	// row 6: 小明 with format 1995/06/28 (should be skipped)
	// Header row 1 is skipped
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
