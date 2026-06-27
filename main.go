package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
	"github.com/robfig/cron/v3"
	"github.com/xuri/excelize/v2"
)

// Config holds all environmental configurations.
type Config struct {
	LineChannelToken  string
	LineChannelSecret string
	AdminLineID       string
	GroupLineID       string
	Port              string
	CronSchedule      string
}

// BirthdayRecord represents a single row parsed from the Excel file.
type BirthdayRecord struct {
	Name     string
	Birthday string // Format: MM-DD
}

func main() {
	log.Println("Starting LINE Bot Birthday Review and Broadcast System...")

	// Load .env file if present (useful for local development)
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or error reading it; using environment variables directly.")
	}

	// 1. Load configuration
	cfg := loadConfig()

	// 2. Load location Asia/Taipei
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		log.Fatalf("Critical error: failed to load timezone Asia/Taipei: %v", err)
	}
	time.Local = loc
	log.Printf("Timezone initialized. Local time in Asia/Taipei is: %s", time.Now().In(loc).Format(time.RFC3339))

	// 3. Initialize LINE Messaging API Client
	bot, err := messaging_api.NewMessagingApiAPI(cfg.LineChannelToken)
	if err != nil {
		log.Fatalf("Critical error: failed to initialize LINE Messaging API Client: %v", err)
	}

	// 4. Start Cron Scheduler
	scheduler := cron.New(cron.WithLocation(loc))
	_, err = scheduler.AddFunc(cfg.CronSchedule, func() {
		log.Println("Scheduled Cron job triggered: running birthday check...")
		checkAndNotify(bot, cfg, loc)
	})
	if err != nil {
		log.Fatalf("Critical error: failed to schedule cron task: %v", err)
	}
	scheduler.Start()
	log.Printf("Cron scheduler started with schedule: '%s' in Asia/Taipei timezone.", cfg.CronSchedule)

	// 5. Setup HTTP Webhook Server and Endpoints
	http.HandleFunc("/callback", makeCallbackHandler(bot, cfg))
	http.HandleFunc("/healthz", makeHealthzHandler())
	http.HandleFunc("/trigger", makeTriggerHandler(bot, cfg, loc)) // Helper endpoint to trigger check manually

	log.Printf("HTTP Server listening on port %s...", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		log.Fatalf("Critical error: HTTP server failed to start: %v", err)
	}
}

// loadConfig loads configuration from environment variables with safety checks.
func loadConfig() Config {
	cfg := Config{
		LineChannelToken:  os.Getenv("LINE_CHANNEL_TOKEN"),
		LineChannelSecret: os.Getenv("LINE_CHANNEL_SECRET"),
		AdminLineID:       os.Getenv("ADMIN_LINE_ID"),
		GroupLineID:       os.Getenv("GROUP_LINE_ID"),
		Port:              os.Getenv("PORT"),
		CronSchedule:      os.Getenv("CRON_SCHEDULE"),
	}

	// Validation
	var missing []string
	if cfg.LineChannelToken == "" {
		missing = append(missing, "LINE_CHANNEL_TOKEN")
	}
	if cfg.LineChannelSecret == "" {
		missing = append(missing, "LINE_CHANNEL_SECRET")
	}
	if cfg.AdminLineID == "" {
		missing = append(missing, "ADMIN_LINE_ID")
	}
	if cfg.GroupLineID == "" {
		missing = append(missing, "GROUP_LINE_ID")
	}

	if len(missing) > 0 {
		log.Printf("Warning: Missing configurations: %s. Some features may fail to function.", strings.Join(missing, ", "))
	}

	// Default fallback values
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.CronSchedule == "" {
		cfg.CronSchedule = "0 9 * * *" // Daily at 09:00 AM
	}

	return cfg
}

// readBirthdays parses the Excel file and extracts valid name & birthday records.
func readBirthdays(filename string) ([]BirthdayRecord, error) {
	f, err := excelize.OpenFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open Excel file %s: %w", filename, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Error closing Excel file: %v", err)
		}
	}()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("no sheets found in Excel file")
	}

	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("failed to read rows from sheet %s: %w", sheets[0], err)
	}

	var records []BirthdayRecord
	for idx, row := range rows {
		// Minimum 2 columns required (Col 1: Name, Col 2: Birthday)
		if len(row) < 2 {
			continue
		}

		name := strings.TrimSpace(row[0])
		birthday := strings.TrimSpace(row[1])

		// Skip header or empty rows dynamically by validating formatting of column 2 (Birthday)
		if name == "" || birthday == "" || !isValidBirthdayFormat(birthday) {
			if idx > 0 && birthday != "" {
				log.Printf("Skipping row %d: invalid birthday format '%s' (must be MM-DD, e.g. 06-28)", idx+1, birthday)
			}
			continue
		}

		records = append(records, BirthdayRecord{
			Name:     name,
			Birthday: birthday,
		})
	}

	log.Printf("Successfully loaded %d valid birthday records from %s.", len(records), filename)
	return records, nil
}

// isValidBirthdayFormat validates whether the string is in MM-DD format.
func isValidBirthdayFormat(val string) bool {
	_, err := time.Parse("01-02", val)
	return err == nil
}

// checkAndNotify scans birthdays and sends review messages to the admin if today/tomorrow contains birthdays.
func checkAndNotify(bot *messaging_api.MessagingApiAPI, cfg Config, loc *time.Location) {
	records, err := readBirthdays("birthdays.xlsx")
	if err != nil {
		log.Printf("Error reading birthdays: %v", err)
		return
	}

	now := time.Now().In(loc)
	todayStr := now.Format("01-02")
	tomorrowStr := now.AddDate(0, 0, 1).Format("01-02")

	var todayNames []string
	var tomorrowNames []string

	for _, record := range records {
		if record.Birthday == todayStr {
			todayNames = append(todayNames, record.Name)
		} else if record.Birthday == tomorrowStr {
			tomorrowNames = append(tomorrowNames, record.Name)
		}
	}

	log.Printf("Birthday Check Result -> Today (%s): %v, Tomorrow (%s): %v", todayStr, todayNames, tomorrowStr, tomorrowNames)

	// Notify admin for today's birthdays if any
	if len(todayNames) > 0 {
		log.Printf("Sending Today's review notification to admin %s", cfg.AdminLineID)
		if err := sendReviewMessage(bot, cfg.AdminLineID, "today", todayStr, todayNames); err != nil {
			log.Printf("Error sending review message for today's birthdays: %v", err)
		}
	}

	// Notify admin for tomorrow's birthdays if any
	if len(tomorrowNames) > 0 {
		log.Printf("Sending Tomorrow's review notification to admin %s", cfg.AdminLineID)
		if err := sendReviewMessage(bot, cfg.AdminLineID, "tomorrow", tomorrowStr, tomorrowNames); err != nil {
			log.Printf("Error sending review message for tomorrow's birthdays: %v", err)
		}
	}
}

// sendReviewMessage constructs and sends a Template Message with a Buttons template to the administrator.
func sendReviewMessage(bot *messaging_api.MessagingApiAPI, adminID string, dayType string, dateStr string, names []string) error {
	dayName := "今天"
	if dayType == "tomorrow" {
		dayName = "明天"
	}

	namesList := strings.Join(names, "、")
	text := fmt.Sprintf("%s (%s) 生日人員：%s\n請審查是否在群組發布祝賀訊息。", dayName, dateStr, namesList)
	// Alt text fallback
	altText := fmt.Sprintf("生日推播審查：%s生日的人有 %s", dayName, namesList)

	// Buttons template text limit is 160 characters. Truncate if name list is too long.
	if len(text) > 160 {
		text = text[:157] + "..."
	}
	if len(altText) > 400 {
		altText = altText[:397] + "..."
	}

	postbackData := fmt.Sprintf("action=publish&type=%s&names=%s", dayType, strings.Join(names, ","))

	message := &messaging_api.TemplateMessage{
		AltText: altText,
		Template: &messaging_api.ButtonsTemplate{
			Title: "生日推播審查",
			Text:  text,
			Actions: []messaging_api.ActionInterface{
				&messaging_api.PostbackAction{
					Label: "確認發布",
					Data:  postbackData,
				},
			},
		},
	}

	_, err := bot.PushMessage(
		&messaging_api.PushMessageRequest{
			To:       adminID,
			Messages: []messaging_api.MessageInterface{message},
		},
		"", // x-line-retry-key
	)
	return err
}

// makeCallbackHandler returns the HTTP handler function for incoming Webhook requests.
func makeCallbackHandler(bot *messaging_api.MessagingApiAPI, cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		cb, err := webhook.ParseRequest(cfg.LineChannelSecret, r)
		if err != nil {
			log.Printf("Webhook ParseRequest failed: %v", err)
			if errors.Is(err, webhook.ErrInvalidSignature) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("Invalid signature"))
			} else {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("Internal server error"))
			}
			return
		}

		for _, event := range cb.Events {
			// Extract and log source information for ID lookup
			var source webhook.SourceInterface
			switch ev := event.(type) {
			case webhook.MessageEvent:
				source = ev.Source
			case *webhook.MessageEvent:
				source = ev.Source
			case webhook.PostbackEvent:
				source = ev.Source
			case *webhook.PostbackEvent:
				source = ev.Source
			case webhook.FollowEvent:
				source = ev.Source
			case *webhook.FollowEvent:
				source = ev.Source
			case webhook.JoinEvent:
				source = ev.Source
			case *webhook.JoinEvent:
				source = ev.Source
			}

			if source != nil {
				switch s := source.(type) {
				case webhook.UserSource:
					log.Printf("[ID Lookup] Event %T received from User (User ID: %s)", event, s.UserId)
				case *webhook.UserSource:
					log.Printf("[ID Lookup] Event %T received from User (User ID: %s)", event, s.UserId)
				case webhook.GroupSource:
					log.Printf("[ID Lookup] Event %T received from Group (Group ID: %s, Sender User ID: %s)", event, s.GroupId, s.UserId)
				case *webhook.GroupSource:
					log.Printf("[ID Lookup] Event %T received from Group (Group ID: %s, Sender User ID: %s)", event, s.GroupId, s.UserId)
				case webhook.RoomSource:
					log.Printf("[ID Lookup] Event %T received from Room (Room ID: %s, Sender User ID: %s)", event, s.RoomId, s.UserId)
				case *webhook.RoomSource:
					log.Printf("[ID Lookup] Event %T received from Room (Room ID: %s, Sender User ID: %s)", event, s.RoomId, s.UserId)
				}
			}

			switch e := event.(type) {
			case webhook.PostbackEvent:
				log.Printf("Postback event received from user %s with data: '%s'", getUserID(e.Source), e.Postback.Data)
				handlePostback(w, bot, cfg, &e)
				return // Early return after handling
			case *webhook.PostbackEvent:
				log.Printf("Postback event received from user %s with data: '%s'", getUserID(e.Source), e.Postback.Data)
				handlePostback(w, bot, cfg, e)
				return // Early return after handling
			default:
				// Ignore other event types quietly
				log.Printf("Ignored event type: %T", event)
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}
}

// handlePostback processes the user postback action, broadcasting to the group if approved.
func handlePostback(w http.ResponseWriter, bot *messaging_api.MessagingApiAPI, cfg Config, e *webhook.PostbackEvent) {
	data := e.Postback.Data
	values, err := url.ParseQuery(data)
	if err != nil {
		log.Printf("Failed to parse postback query data: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	action := values.Get("action")
	if action != "publish" {
		log.Printf("Ignored postback action: %s", action)
		w.WriteHeader(http.StatusOK)
		return
	}

	dayType := values.Get("type")
	namesStr := values.Get("names")
	if namesStr == "" {
		log.Println("Postback contained empty name list.")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	names := strings.Split(namesStr, ",")
	formattedNames := strings.Join(names, "、")

	var greeting string
	if dayType == "tomorrow" {
		greeting = fmt.Sprintf("祝明天生日的 %s 生日快樂！🎉🎂", formattedNames)
	} else {
		// default to today
		greeting = fmt.Sprintf("祝今天生日的 %s 生日快樂！🎉🎂", formattedNames)
	}

	// 1. Broadcast greeting to target Group
	log.Printf("Broadcasting greeting message to group %s", cfg.GroupLineID)
	_, err = bot.PushMessage(
		&messaging_api.PushMessageRequest{
			To: cfg.GroupLineID,
			Messages: []messaging_api.MessageInterface{
				&messaging_api.TextMessage{
					Text: greeting,
				},
			},
		},
		"",
	)
	if err != nil {
		log.Printf("Failed to push greeting message to group: %v", err)
		// Notify the admin of the failure
		replyFailure(bot, e.ReplyToken, fmt.Sprintf("發布失敗：%v", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// 2. Reply to admin indicating success
	log.Println("Broadcast succeeded. Replying success status to admin.")
	replySuccess(bot, e.ReplyToken, fmt.Sprintf("生日祝賀已成功發布！\n內容：%s", greeting))
	w.WriteHeader(http.StatusOK)
}

// replySuccess replies to the event indicating a successful operation.
func replySuccess(bot *messaging_api.MessagingApiAPI, replyToken string, text string) {
	_, err := bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: replyToken,
		Messages: []messaging_api.MessageInterface{
			&messaging_api.TextMessage{
				Text: text,
			},
		},
	})
	if err != nil {
		log.Printf("Failed to send success reply message: %v", err)
	}
}

// replyFailure replies to the event indicating a failed operation.
func replyFailure(bot *messaging_api.MessagingApiAPI, replyToken string, text string) {
	_, err := bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: replyToken,
		Messages: []messaging_api.MessageInterface{
			&messaging_api.TextMessage{
				Text: text,
			},
		},
	})
	if err != nil {
		log.Printf("Failed to send failure reply message: %v", err)
	}
}

// makeHealthzHandler returns a basic healthcheck handler.
func makeHealthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Healthy"))
	}
}

// makeTriggerHandler returns an HTTP handler to manually invoke the birthday check.
func makeTriggerHandler(bot *messaging_api.MessagingApiAPI, cfg Config, loc *time.Location) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Println("Manual trigger endpoint called. Running birthday check...")
		checkAndNotify(bot, cfg, loc)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Manual check triggered successfully. Admin notified if birthdays exist."))
	}
}

// getUserID extracts the user ID from various source types in LINE v8 webhook events.
func getUserID(source webhook.SourceInterface) string {
	if source == nil {
		return ""
	}
	switch s := source.(type) {
	case *webhook.UserSource:
		return s.UserId
	case *webhook.GroupSource:
		return s.UserId
	case *webhook.RoomSource:
		return s.UserId
	}
	return ""
}

