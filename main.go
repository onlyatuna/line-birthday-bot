package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	ExcelPath         string
}

// BirthdayRecord represents a single row parsed from the Excel file.
type BirthdayRecord struct {
	Name     string
	Birthday string // Format: MM-DD
}

// RuntimeConfig holds dynamic settings modified via chat commands.
type RuntimeConfig struct {
	GroupLineID      string `json:"group_line_id"`
	GreetingTemplate string `json:"greeting_template"`
}

var (
	runtimeConfig     RuntimeConfig
	runtimeConfigMu   sync.RWMutex
	runtimeConfigPath = "data/config.json"
)

func main() {
	log.Println("Starting LINE Bot Birthday Review and Broadcast System...")

	// Load .env file if present (useful for local development)
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found or error reading it; using environment variables directly.")
	}

	// 1. Load configuration
	cfg := loadConfig()

	// Load dynamic runtime config (falling back to env GROUP_LINE_ID if config.json not set)
	loadRuntimeConfig(cfg.GroupLineID)

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
		ExcelPath:         os.Getenv("EXCEL_PATH"),
	}

	if cfg.ExcelPath == "" {
		cfg.ExcelPath = "data/birthdays.xlsx"
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
	records, err := readBirthdays(cfg.ExcelPath)
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
			case webhook.MessageEvent:
				handleMessage(w, bot, cfg, &e)
				return
			case *webhook.MessageEvent:
				handleMessage(w, bot, cfg, e)
				return
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

	dayWord := "今天"
	if dayType == "tomorrow" {
		dayWord = "明天"
	}
	template := getGreetingTemplate()
	greeting := strings.ReplaceAll(template, "{day}", dayWord)
	greeting = strings.ReplaceAll(greeting, "{names}", formattedNames)

	// 1. Broadcast greeting to target Group
	targetGroup := getGroupLineID()
	log.Printf("Broadcasting greeting message to group %s", targetGroup)
	_, err = bot.PushMessage(
		&messaging_api.PushMessageRequest{
			To: targetGroup,
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

// loadRuntimeConfig reads config.json or populates default settings.
func loadRuntimeConfig(fallbackGroup string) {
	runtimeConfigMu.Lock()
	defer runtimeConfigMu.Unlock()

	runtimeConfig.GroupLineID = fallbackGroup
	runtimeConfig.GreetingTemplate = "祝{day}生日的 {names} 生日快樂！🎉🎂"

	file, err := os.Open(runtimeConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("%s does not exist. Initializing with default values.", runtimeConfigPath)
			dir := filepath.Dir(runtimeConfigPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				log.Printf("Failed to create directory %s: %v", dir, err)
			}
			data, _ := json.MarshalIndent(runtimeConfig, "", "  ")
			_ = os.WriteFile(runtimeConfigPath, data, 0644)
			return
		}
		log.Printf("Failed to open %s: %v", runtimeConfigPath, err)
		return
	}
	defer file.Close()

	var loaded RuntimeConfig
	if err := json.NewDecoder(file).Decode(&loaded); err != nil {
		log.Printf("Failed to decode %s: %v", runtimeConfigPath, err)
		return
	}

	if loaded.GroupLineID != "" {
		runtimeConfig.GroupLineID = loaded.GroupLineID
	}
	if loaded.GreetingTemplate != "" {
		runtimeConfig.GreetingTemplate = loaded.GreetingTemplate
	}
	log.Printf("Successfully loaded runtime config from %s. Group: %s, Template: %s", runtimeConfigPath, runtimeConfig.GroupLineID, runtimeConfig.GreetingTemplate)
}

// saveRuntimeConfig writes current runtime settings to config.json.
func saveRuntimeConfig() error {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()

	data, err := json.MarshalIndent(runtimeConfig, "", "  ")
	if err != nil {
		return err
	}
	
	dir := filepath.Dir(runtimeConfigPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("Failed to create directory %s: %v", dir, err)
	}
	
	return os.WriteFile(runtimeConfigPath, data, 0644)
}

func getGroupLineID() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return runtimeConfig.GroupLineID
}

func getGreetingTemplate() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return runtimeConfig.GreetingTemplate
}

func setGroupLineID(groupID string) error {
	runtimeConfigMu.Lock()
	runtimeConfig.GroupLineID = groupID
	runtimeConfigMu.Unlock()
	return saveRuntimeConfig()
}

func setGreetingTemplate(temp string) error {
	runtimeConfigMu.Lock()
	runtimeConfig.GreetingTemplate = temp
	runtimeConfigMu.Unlock()
	return saveRuntimeConfig()
}

// handleMessage processes text messages, routing command structures if sent by Admin.
func handleMessage(w http.ResponseWriter, bot *messaging_api.MessagingApiAPI, cfg Config, e *webhook.MessageEvent) {
	var isAdminPrivateChat bool
	if e.Source != nil {
		switch s := e.Source.(type) {
		case *webhook.UserSource:
			if s.UserId == cfg.AdminLineID {
				isAdminPrivateChat = true
			}
		case webhook.UserSource:
			if s.UserId == cfg.AdminLineID {
				isAdminPrivateChat = true
			}
		}
	}

	if !isAdminPrivateChat {
		w.WriteHeader(http.StatusOK)
		return
	}

	switch message := e.Message.(type) {
	case *webhook.TextMessageContent:
		text := message.Text
		if strings.HasPrefix(text, "/") {
			log.Printf("Admin sent command: '%s'", text)
			handleAdminCommand(bot, e.ReplyToken, text)
		}
	case webhook.TextMessageContent:
		text := message.Text
		if strings.HasPrefix(text, "/") {
			log.Printf("Admin sent command: '%s'", text)
			handleAdminCommand(bot, e.ReplyToken, text)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// handleAdminCommand routes and executes slash commands.
func handleAdminCommand(bot *messaging_api.MessagingApiAPI, replyToken string, text string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return
	}

	parts := strings.SplitN(text, " ", 2)
	command := parts[0]
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	switch command {
	case "/help":
		helpText := "可用指令：\n" +
			"1. /show : 顯示目前群組 ID 與祝賀詞範本\n" +
			"2. /setgroup [群組ID] : 設定發送群組\n" +
			"3. /settemplate [範本] : 設定祝賀詞範本（可用 {day} 與 {names}）\n" +
			"4. /help : 顯示此幫助訊息"
		replyText(bot, replyToken, helpText)

	case "/show":
		group := getGroupLineID()
		template := getGreetingTemplate()
		info := fmt.Sprintf("目前設定：\n- 群組 ID: %s\n- 祝賀詞範本: %s", group, template)
		replyText(bot, replyToken, info)

	case "/setgroup":
		if args == "" {
			replyText(bot, replyToken, "請提供群組 ID，例如：\n/setgroup Cxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			return
		}
		if !strings.HasPrefix(args, "C") || len(args) != 33 {
			replyText(bot, replyToken, "無效認證格式：通常以 'C' 開頭，共 33 碼的群組 ID")
			return
		}
		if err := setGroupLineID(args); err != nil {
			replyText(bot, replyToken, fmt.Sprintf("儲存群組 ID 失敗：%v", err))
			return
		}
		replyText(bot, replyToken, fmt.Sprintf("設定群組 ID 成功！\n目前群組 ID：%s", args))

	case "/settemplate":
		if args == "" {
			replyText(bot, replyToken, "請提供祝賀詞範本，例如：\n/settemplate 祝{day}生日的 {names} 生日快樂！🎉")
			return
		}
		if err := setGreetingTemplate(args); err != nil {
			replyText(bot, replyToken, fmt.Sprintf("儲存祝賀詞範本失敗：%v", err))
			return
		}
		replyText(bot, replyToken, fmt.Sprintf("設定祝賀詞範本成功！\n目前範本：%s", args))

	default:
		replyText(bot, replyToken, fmt.Sprintf("未知指令 '%s'，輸入 /help 查看可用指令。", command))
	}
}

func replyText(bot *messaging_api.MessagingApiAPI, replyToken string, text string) {
	if bot == nil {
		log.Printf("[Test Mode] Reply text: %q", text)
		return
	}
	_, err := bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: replyToken,
		Messages: []messaging_api.MessageInterface{
			&messaging_api.TextMessage{
				Text: text,
			},
		},
	})
	if err != nil {
		log.Printf("Failed to reply message: %v", err)
	}
}


