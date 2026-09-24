package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nice-pink/itakeit/pkg/bot"
	"github.com/nice-pink/itakeit/pkg/config"
	"github.com/nice-pink/itakeit/pkg/store"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to the config file")
	debug := flag.Bool("debug", false, "log raw Socket Mode traffic")
	flag.Parse()

	if err := run(*cfgPath, *debug); err != nil {
		slog.Error("itakeit stopped", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, debug bool) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	botToken, appToken := os.Getenv("SLACK_BOT_TOKEN"), os.Getenv("SLACK_APP_TOKEN")
	if !strings.HasPrefix(botToken, "xoxb-") || !strings.HasPrefix(appToken, "xapp-") {
		return errMissingTokens
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	api := slack.New(botToken, slack.OptionAppLevelToken(appToken), slack.OptionDebug(debug),
		slack.OptionHTTPClient(&http.Client{Timeout: 15 * time.Second}), slack.OptionRetry(3))
	auth, err := api.AuthTest()
	if err != nil {
		return err
	}
	slog.Info("authenticated", "team", auth.Team, "bot_user", auth.UserID, "channel", cfg.Channel)

	sm := socketmode.New(api, socketmode.OptionDebug(debug))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return bot.New(api, st, cfg, auth.UserID, auth.BotID).Run(ctx, sm)
}

var errMissingTokens = errors.New("set SLACK_BOT_TOKEN (xoxb-...) and SLACK_APP_TOKEN (xapp-...)")
