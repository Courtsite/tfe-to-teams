package function

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	triggerToColour = map[string]string{
		"run:created":         "#595959",
		"run:planning":        "#13c2c2",
		"run:needs_attention": "#fadb14",
		"run:applying":        "#1890ff",
		"run:completed":       "#a0d911",
		"run:errored":         "#f5222d",
	}
)

type TFENotificationPayload struct {
	PayloadVersion              int               `json:"payload_version"`
	NotificationConfigurationID string            `json:"notification_configuration_id"`
	RunURL                      string            `json:"run_url"`
	RunID                       string            `json:"run_id"`
	RunMessage                  string            `json:"run_message"`
	RunCreatedAt                time.Time         `json:"run_created_at"`
	RunCreatedBy                string            `json:"run_created_by"`
	WorkspaceID                 string            `json:"workspace_id"`
	WorkspaceName               string            `json:"workspace_name"`
	OrganizationName            string            `json:"organization_name"`
	Notifications               []TFENotification `json:"notifications"`
}

type TFENotification struct {
	Message      string    `json:"message"`
	Trigger      string    `json:"trigger"`
	RunStatus    string    `json:"run_status"`
	RunUpdatedAt time.Time `json:"run_updated_at"`
	RunUpdatedBy string    `json:"run_updated_by"`
}

type MessageCard struct {
	Type             string            `json:"@type"`
	Context          string            `json:"@context"`
	Summary          string            `json:"summary,omitempty"`
	Title            string            `json:"title,omitempty"`
	Text             string            `json:"text,omitempty"`
	ThemeColor       string            `json:"themeColor,omitempty"`
	Sections         []Section         `json:"sections,omitempty"`
	PotentialActions []PotentialAction `json:"potentialAction,omitempty"`
}

type Section struct {
	ActivityTitle    string `json:"activityTitle,omitempty"`
	ActivitySubtitle string `json:"activitySubtitle,omitempty"`
	Facts            []Fact `json:"facts,omitempty"`
}

type Fact struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type PotentialAction struct {
	Type    string              `json:"@type"`
	Name    string              `json:"name"`
	Targets []map[string]string `json:"targets,omitempty"`
}

func toTeams(payload TFENotificationPayload) MessageCard {
	notification := payload.Notifications[0]

	title := notification.Message
	if payload.WorkspaceName != "" {
		title = fmt.Sprintf("%s in %s.", notification.Message, payload.WorkspaceName)
	}

	facts := []Fact{}

	if payload.OrganizationName != "" {
		facts = append(facts, Fact{
			Name:  "Organisation",
			Value: payload.OrganizationName,
		})
	}

	if payload.RunID != "" {
		facts = append(facts, Fact{
			Name:  "Run ID",
			Value: payload.RunID,
		})
	}

	if payload.RunCreatedBy != "" {
		facts = append(facts, Fact{
			Name:  "Run Created By",
			Value: payload.RunCreatedBy,
		})
	}

	if !payload.RunCreatedAt.IsZero() {
		facts = append(facts, Fact{
			Name:  "Run Created At",
			Value: payload.RunCreatedAt.Format("02/01/2006 15:04:05"),
		})
	}

	colour := triggerToColour[notification.Trigger]
	log.Println("unsupported trigger:", notification.Trigger)

	section := Section{
		Facts: facts,
	}

	if notification.RunUpdatedBy != "" {
		section.ActivityTitle = notification.RunUpdatedBy
	}

	if !notification.RunUpdatedAt.IsZero() {
		section.ActivitySubtitle = notification.RunUpdatedAt.Format("02/01/2006 15:04:05")
	}

	return MessageCard{
		Type:       "MessageCard",
		Context:    "https://schema.org/extensions",
		Summary:    title,
		Title:      title,
		Text:       payload.RunMessage,
		ThemeColor: colour,
		Sections:   []Section{section},
		PotentialActions: []PotentialAction{
			{
				Type: "OpenUri",
				Name: "View Run",
				Targets: []map[string]string{
					{
						"os":  "default",
						"uri": payload.RunURL,
					},
				},
			},
		},
	}
}

func F(w http.ResponseWriter, r *http.Request) {
	teamsWebhookURL := os.Getenv("TEAMS_WEBHOOK_URL")
	if teamsWebhookURL == "" {
		log.Fatalln("`TEAMS_WEBHOOK_URL` is not set in the environment")
	}

	if _, err := url.Parse(teamsWebhookURL); err != nil {
		log.Fatalln(err)
	}

	if contentType := r.Header.Get("Content-Type"); r.Method != "POST" || contentType != "application/json" {
		log.Printf("\ninvalid method / content-type: %s / %s \n", r.Method, contentType)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid request"))
		return
	}

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Fatalln(err)
	}

	tfeWebhookToken := os.Getenv("TFE_WEBHOOK_TOKEN")

	// https://www.terraform.io/docs/cloud/api/notification-configurations.html#notification-authenticity
	if tfeNotificationSignature := strings.TrimSpace(r.Header.Get("X-TFE-Notification-Signature")); tfeNotificationSignature != "" {
		if tfeWebhookToken == "" {
			log.Fatalln("received notification with signature, but `TFE_WEBHOOK_TOKEN` was not set in the environment")
		}

		mac := hmac.New(sha512.New, []byte(strings.TrimSpace(tfeWebhookToken)))
		_, err = mac.Write(data)
		if err != nil {
			log.Fatalln(err)
		}
		expectedMAC := mac.Sum(nil)

		tfeNotificationHexSignature, err := hex.DecodeString(tfeNotificationSignature)
		if err != nil {
			log.Fatalln(err)
		}

		if !hmac.Equal(tfeNotificationHexSignature, expectedMAC) {
			log.Printf("\nsignature does not match: %s (got) != %s (want) \n", hex.EncodeToString(tfeNotificationHexSignature), hex.EncodeToString(expectedMAC))
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid request"))
			return
		}
	}

	var payload TFENotificationPayload
	err = json.Unmarshal(data, &payload)
	if err != nil {
		log.Printf("\nraw data received: %q \n", data)
		log.Fatalln(err)
	}

	if payload.PayloadVersion != 1 {
		log.Println("version not supported:", payload.PayloadVersion)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid request"))
		return
	}

	teamsWebhook := toTeams(payload)

	teamsPayload, err := json.Marshal(teamsWebhook)
	if err != nil {
		log.Fatalln(err)
	}

	res, err := http.Post(teamsWebhookURL, "application/json", bytes.NewBuffer(teamsPayload))
	if err != nil {
		log.Fatalln(err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		log.Println("payload", string(teamsPayload))
		log.Fatalln("unexpected status code", res.StatusCode)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	err = json.NewEncoder(w).Encode(teamsWebhook)
	if err != nil {
		log.Fatalln(err)
	}
}
