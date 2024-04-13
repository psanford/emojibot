package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/psanford/lambdahttp/lambdahttpv2"
	"github.com/psanford/logmiddleware"
	"github.com/psanford/ssmparam"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

var (
	addr    = flag.String("listen-addr", "127.0.0.1:1234", "Host/Port to listen on")
	cliMode = flag.String("mode", "", "execution mode: http|lambda")
)

func main() {
	flag.Parse()

	sess := session.Must(session.NewSession())
	kv := ssmparam.New(ssm.New(sess))

	signingSecret, err := kv.Get("SLACK_SIGNING_SECRET")
	if err != nil {
		log.Fatalf("SLACK_SIGNING_SECRET not set in env or in parameter store")
	}

	token, err := kv.Get("SLACK_TOKEN")
	if err != nil {
		log.Fatal("SLACK_TOKEN not set in env or in parameter store")
	}

	channel, err := kv.Get("SLACK_CHANNEL_ID")
	if err != nil {
		log.Fatal("SLACK_CHANNEL_ID not set in env or in parameter store")
	}

	s := server{
		signingSecret: signingSecret,
		slack:         slack.New(token),
		channelID:     channel,
	}

	m := http.NewServeMux()
	m.HandleFunc("/", s.HandleRequest)

	switch *cliMode {
	case "http":
		fmt.Printf("Listening on %s\n", *addr)
		panic(http.ListenAndServe(*addr, logmiddleware.New(m)))
	default:
		lambda.Start(lambdahttpv2.NewLambdaHandler(logmiddleware.New(m)))
	}
}

type server struct {
	signingSecret string
	slack         *slack.Client
	channelID     string
}

func (s *server) HandleRequest(w http.ResponseWriter, r *http.Request) {
	lgr := logmiddleware.LgrFromContext(r.Context())

	body, err := s.validateSignature(r)
	if err != nil {
		lgr.Error("signature_validation_failed", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	eventsAPIEvent, err := slackevents.ParseEvent(json.RawMessage(body), slackevents.OptionNoVerifyToken())
	if err != nil {
		lgr.Error("parse_event_err", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	switch eventsAPIEvent.Type {
	case slackevents.URLVerification:
		evt := eventsAPIEvent.Data.(*slackevents.EventsAPIURLVerificationEvent)
		w.Header().Set("Content-Type", "text")
		w.Write([]byte(evt.Challenge))
		return
	case slackevents.CallbackEvent:

		switch eventsAPIEvent.InnerEvent.Type {
		case slackevents.EmojiChanged:
			evt := eventsAPIEvent.InnerEvent.Data.(*slackevents.EmojiChangedEvent)
			if evt.Subtype == "add" {
				lgr.Info("got_new_emoji", "payload", evt)
				attachment := slack.Attachment{
					Text:     evt.Name,
					ImageURL: evt.Value,
				}
				_, _, err = s.slack.PostMessage(
					s.channelID,
					slack.MsgOptionText(fmt.Sprintf("New emoji: %s", evt.Name), false),
					slack.MsgOptionAttachments(attachment),
					slack.MsgOptionAsUser(true),
				)

				if err != nil {
					lgr.Error("post_to_slack_err", "err", err)
				}
			}
		default:
			lgr.Info("unexpected_callback_event", "inner", eventsAPIEvent.InnerEvent.Type)
		}
	default:
		lgr.Info("unexpected_event", "evt", eventsAPIEvent.Type, "inner", eventsAPIEvent.InnerEvent.Type)
	}
}

type slackEvent struct {
	Type string `json:"type"`
}

func (s *server) validateSignature(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	sv, err := slack.NewSecretsVerifier(r.Header, s.signingSecret)
	if err != nil {
		return nil, err
	}
	if _, err := sv.Write(body); err != nil {
		return nil, err
	}
	if err := sv.Ensure(); err != nil {
		return nil, err
	}

	return body, nil
}
