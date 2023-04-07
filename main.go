package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/eatmoreapple/env"
	"github.com/eatmoreapple/openai"
	"github.com/eatmoreapple/openwechat"
	"github.com/patrickmn/go-cache"
)

var (
	appID     = env.Name("APP_ID").StringOrElse("b136e65089c447b4b5cb1f7c551c6c4f")
	appSecret = env.Name("APP_SECRET").StringOrElse("4c343a7d9c2e41fab9eeb9fdbad486b1")

	// history is the cache of messages.
	history = cache.New(time.Minute*10, time.Minute)
)

// requestAkHook is the hook to add access token to request.
func requestAkHook(token string) openai.RequestHooker {
	return func(req *http.Request) {
		query := req.URL.Query()
		query.Set("access_token", token)
		req.URL.RawQuery = query.Encode()
	}
}

// GPTClient represents the client of GPT.
type GPTClient struct {
	appID     string
	appSecret string
}

// Completion requests the completion of the message.
func (c GPTClient) Completion(ctx context.Context, history openai.CompletionMessages) (string, error) {
	ak, err := c.getAccessToken()
	if err != nil {
		return "", err
	}
	req := openai.CompletionRequest{
		Messages: history,
		Model:    openai.CompletionModelGPT35Turbo,
	}
	client := openai.DefaultClient("")
	host, err := url.Parse("http://cpdd.today")
	if err != nil {
		return "", err
	}
	client.BaseURL = host
	client.AddRequestHooker(requestAkHook(ak))
	resp, err := client.Completion(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.MessageContent(), nil
}

func (c GPTClient) getAccessToken() (string, error) {
	values := url.Values{}
	values.Add("app_id", c.appID)
	values.Add("app_secret", c.appSecret)
	req, err := http.NewRequest(http.MethodGet, "http://cpdd.today/auth/ak", nil)
	if err != nil {
		return "", err
	}
	req.URL.RawQuery = values.Encode()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		Ak       string `json:"access_token"`
		ExpireIn int    `json:"expire_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Ak, nil
}

type Replier interface {
	Reply(msg *openwechat.Message) error
}

// FriendReplier replies to friend messages.
type FriendReplier struct{}

func (r *FriendReplier) Reply(msg *openwechat.Message) error {
	sender, err := msg.Sender()
	if err != nil {
		return err
	}
	replier := GPTReplier{key: sender.UserName}
	return replier.Reply(msg)
}

// GroupReplier replies to group messages.
type GroupReplier struct{}

func (r *GroupReplier) Reply(msg *openwechat.Message) error {
	sender, err := msg.SenderInGroup()
	if err != nil {
		return err
	}
	replier := GPTReplier{key: sender.UserName}
	return replier.Reply(msg)
}

type GPTReplier struct {
	key string
}

func (r *GPTReplier) Reply(msg *openwechat.Message) error {
	if !msg.IsText() {
		return nil
	}
	item, exists := history.Get(r.key)
	if !exists {
		item = openai.CompletionMessages{}
	}
	messages := item.(openai.CompletionMessages)
	messages = append(messages, openai.CompletionMessage{Role: openai.RoleUser, Content: msg.Content})
	client := &GPTClient{appID: appID, appSecret: appSecret}
	resp, err := client.Completion(context.Background(), messages)
	if err != nil {
		_, err = msg.ReplyText(err.Error())
		return err
	}
	_, err = msg.ReplyText(resp)
	if len(messages) > 20 {
		messages = messages[len(messages)-20:]
	}
	history.Set(r.key, messages, time.Minute*10)
	return err
}

func main() {
	bot := openwechat.DefaultBot(openwechat.Desktop)
	storage := openwechat.NewFileHotReloadStorage("storage.json")
	defer func() { _ = storage.Close() }()
	if err := bot.PushLogin(storage, openwechat.NewRetryLoginOption()); err != nil {
		log.Fatal(err)
	}
	dispatcher := openwechat.NewMessageMatchDispatcher()
	dispatcher.OnFriend(func(msg *openwechat.MessageContext) {
		var replier Replier = &FriendReplier{}
		if err := replier.Reply(msg.Message); err != nil {
			log.Println(err)
		}
	})
	dispatcher.OnGroup(func(ctx *openwechat.MessageContext) {
		if ctx.IsAt() && strings.Contains(ctx.Content, ctx.Owner().NickName) {
			var replier Replier = &GroupReplier{}
			if err := replier.Reply(ctx.Message); err != nil {
				log.Println(err)
			}
		}
	})
	dispatcher.SetAsync(true)
	bot.MessageHandler = dispatcher.AsMessageHandler()
	log.Fatal(bot.Block())
}
