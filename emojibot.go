package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/inconshreveable/log15"
	"github.com/psanford/lambdahttp/lambdahttpv2"
	"github.com/psanford/logmiddleware"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

var (
	addr    = flag.String("listen-addr", "127.0.0.1:1234", "Host/Port to listen on")
	cliMode = flag.String("mode", "", "execution mode: http|lambda")
)

func main() {
	flag.Parse()
	handler := log15.StreamHandler(os.Stdout, log15.LogfmtFormat())
	log15.Root().SetHandler(handler)

	kv := newKV()

	signingSecret, err := kv.get("SLACK_SIGNING_SECRET")
	if err != nil {
		log.Fatalf("SLACK_SIGNING_SECRET not set in env or in parameter store")
	}

	token, err := kv.get("SLACK_TOKEN")
	if err != nil {
		log.Fatal("SLACK_TOKEN not set in env or in parameter store")
	}

	channel, err := kv.get("SLACK_CHANNEL_ID")
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
	body, err := ioutil.ReadAll(r.Body)
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

func (kv *kv) mustGet(key string) string {
	v, err := kv.get(key)
	if err != nil {
		panic(err)
	}
	return v
}

func (kv *kv) get(key string) (string, error) {
	v := os.Getenv(key)
	if v != "" {
		return v, nil
	}

	ssmPath := os.Getenv("SSM_PATH")
	if ssmPath == "" {
		return "", errors.New("SSM_PATH not set")
	}
	p := path.Join(ssmPath, key)

	req := ssm.GetParameterInput{
		Name:           &p,
		WithDecryption: aws.Bool(true),
	}

	resp, err := kv.client.GetParameter(&req)
	if err != nil {
		return "", fmt.Errorf("read key %s err: %w", key, err)
	}
	val := resp.Parameter.Value
	if val == nil {
		return "", errors.New("value is nil")
	}
	return *val, nil
}

func newKV() *kv {
	sess := session.Must(session.NewSession())
	ssmClient := ssm.New(sess)

	return &kv{
		client: ssmClient,
	}
}

type kv struct {
	client *ssm.SSM
}
