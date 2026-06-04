package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

func openShell(ctx context.Context, keys *input.Input, msg *cmdg.Message, thread *cmdg.Thread) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	// Gather message metadata.
	msgID, _ := msg.GetHeader(ctx, "Message-ID")
	// Message-ID values are conventionally wrapped in angle brackets;
	// strip them so the env var holds the bare ID.
	msgID = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(msgID), "<"), ">")
	subject, _ := msg.GetSubject(ctx)
	from, _ := msg.GetFrom(ctx)
	date, _ := msg.GetDateHeader(ctx)
	to, _ := msg.GetHeader(ctx, "To")
	cc, _ := msg.GetHeader(ctx, "CC")
	references, _ := msg.GetHeader(ctx, "References")

	keys.Stop()
	defer func() {
		if err := keys.Start(); err != nil {
			log.Errorf("Failed to restart input: %v", err)
		}
	}()

	// cmdg disables line wrapping and leaves stale view content on the
	// terminal. A plain shell won't clear it on its own, so reset wrapping
	// and clear the screen before handing over control.
	fmt.Print(display.DoWrap + display.ClearScreen)

	cmd := exec.CommandContext(ctx, shell)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"CMDG_MSG_ID="+msgID,
		"CMDG_MSG_SUBJECT="+subject,
		"CMDG_MSG_FROM="+from,
		"CMDG_MSG_DATE="+date,
		"CMDG_MSG_TO="+to,
		"CMDG_MSG_CC="+cc,
		"CMDG_MSG_REFERENCES="+references,
		"CMDG_THREAD_ID="+thread.ID,
	)
	if err := cmd.Start(); err != nil {
		return errors.Wrapf(err, "failed to start shell %q", shell)
	}
	if err := cmd.Wait(); err != nil {
		return errors.Wrapf(err, "shell %q failed", shell)
	}
	return nil
}
