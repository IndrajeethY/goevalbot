package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
)

func main() {
	var token string
	fmt.Print("Enter bot token: ")
	fmt.Scanln(&token)
	if token == "" {
		panic("TOKEN input is empty")
	}
	b, err := gotgbot.NewBot(token, nil)
	if err != nil {
		panic("failed to create new bot: " + err.Error())
	}

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(b *gotgbot.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			log.Println("an error occurred while handling update:", err.Error())
			return ext.DispatcherActionNoop
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	})
	updater := ext.NewUpdater(dispatcher, nil)
	dispatcher.AddHandler(handlers.NewCommand("start", Start))
	dispatcher.AddHandler(handlers.NewCommand("eval", evalHandler))
	dispatcher.AddHandler(handlers.NewCommand("exec", execHandler))

	err = updater.StartPolling(b, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout: 9,
			RequestOpts: &gotgbot.RequestOpts{
				Timeout: time.Second * 10,
			},
		},
	})
	if err != nil {
		panic("failed to start polling: " + err.Error())
	}
	log.Printf("%s has been started...\n", b.User.Username)

	updater.Idle()
}

func Start(b *gotgbot.Bot, ctx *ext.Context) error {
	_, err := ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Hello, I'm %s. Just a go Eval Bot!", b.User.FirstName), nil)
	if err != nil {
		return fmt.Errorf("failed to send start message: %w", err)
	}
	return nil
}

func evalHandler(b *gotgbot.Bot, ctx *ext.Context) error {
	user := ctx.EffectiveSender.User
	msg := ctx.EffectiveMessage

	code := strings.TrimPrefix(ctx.Message.Text, "/eval ")
	if user.Id != 1960469142 {
		return ext.EndGroups
	}
	if code == "" {
		_, err := msg.Reply(b, "Usage: <code>/eval <Go code></code>", &gotgbot.SendMessageOpts{
			ParseMode: "HTML",
		})
		return err
	}
	evalFunc := func(code string) (string, error) {
		output, evalErr := PerformEval(code, b, ctx)
		if evalErr != "" {
			return "", errors.New(evalErr)
		}
		return output, nil
	}

	return executeCommand(b, ctx, code, "Eval Code", evalFunc)
}

func execHandler(b *gotgbot.Bot, ctx *ext.Context) error {
	user := ctx.EffectiveSender.User
	msg := ctx.EffectiveMessage
	code := strings.TrimPrefix(msg.Text, "/exec ")
	if user.Id != 1960469142 {
		return ext.EndGroups
	}
	if code == "" {
		_, err := msg.Reply(b, "Usage: <code>/exec <Go code></code>", &gotgbot.SendMessageOpts{
			ParseMode: "HTML",
		})
		return err
	}

	return executeCommand(b, ctx, code, "Exec Code", RunCommand)
}

func executeCommand(b *gotgbot.Bot, ctx *ext.Context, code, codeType string, commandFunc func(string) (string, error)) error {
	msg, _ := ctx.Message.Reply(b, "<code>Running...</code>", &gotgbot.SendMessageOpts{
		ParseMode: "HTML",
	})

	output, cmdErr := commandFunc(code)
	if cmdErr != nil {
		_, _, err := msg.EditText(b, fmt.Sprintf("<b>%s:</b>\n<pre>%s</pre>\n\n<b>Error:</b>\n<pre>%s</pre>", codeType, html.EscapeString(code), cmdErr.Error()), &gotgbot.EditMessageTextOpts{
			ParseMode: "HTML",
		})
		return err
	}

	if len(output) > 3000 {
		return SendOutputAsFile(b, ctx, code, output, msg, codeType)
	}

	_, _, err := msg.EditText(b, fmt.Sprintf("<b>%s:</b>\n<pre>%s</pre>\n\n<b>Output:</b>\n<pre>%s</pre>", codeType, html.EscapeString(code), output), &gotgbot.EditMessageTextOpts{
		ParseMode: "HTML",
	})
	return err
}

func PerformEval(code string, b *gotgbot.Bot, ctx *ext.Context) (string, string) {
	ctxJSON, _ := json.Marshal(ctx)
	boilerCode := `
package main

import (
	"fmt"
	"log"
	"encoding/json"
	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"net/http"
)

var b *gotgbot.Bot
var r *gotgbot.Message
var m *gotgbot.Message
var ctx *ext.Context
var chat *gotgbot.Chat
var sender *gotgbot.User

var p func(...any) = func(a ...any) {
	for i, v := range a {
		valueType := reflect.TypeOf(v)
		if valueType.Kind() == reflect.Struct || (valueType.Kind() == reflect.Ptr && valueType.Elem().Kind() == reflect.Struct) {
			jsonData, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				a[i] = "Error marshalling struct"
			} else {
				a[i] = string(jsonData)
			}
		}
	}
	fmt.Println(a...)
}

func init() {
	token := "%s" 
	var err error
	b, err = gotgbot.NewBot(token, &gotgbot.BotOpts{
		BotClient: &gotgbot.BaseBotClient{
			Client: http.Client{},
		},
	})
	if err != nil {
		log.Fatal("Failed to initialize bot: ", err)
	}

	ctxJSON := %s
	err = json.Unmarshal([]byte(ctxJSON), &ctx)
	if err != nil {
		log.Fatal("Failed to unmarshal context: ", err)
	}
	m = ctx.EffectiveMessage
	r = m.ReplyToMessage
	sender = ctx.EffectiveUser
	chat = ctx.EffectiveChat
}

func main() {
	%s
}`
	goCode := fmt.Sprintf(boilerCode, b.Token, strconv.Quote(string(ctxJSON)), code)
	tmpDir, err := os.MkdirTemp("", "eval")
	if err != nil {
		return "", fmt.Sprintf("Error creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpFileName := filepath.Join(tmpDir, "eval.go")
	err = os.WriteFile(tmpFileName, []byte(goCode), 0644)
	if err != nil {
		return "", fmt.Sprintf("Error writing file: %v", err)
	}

	return runGoCode(tmpFileName)
}

func runGoCode(fileName string) (string, string) {
	var stdout, stderr bytes.Buffer
	cmdGoImports := exec.Command("goimports", "-w", fileName)
	cmdGoImports.Stderr = &stderr
	err := cmdGoImports.Run()
	if err != nil {
		return "", fmt.Sprintf("Error running goimports: %v\nStderr: %s", err, stderr.String())
	}
	cmd := exec.Command("go", "run", fileName)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return "", fmt.Sprintf("Error: %v\n%s", err, stderr.String())
	}
	return html.EscapeString(stdout.String()), ""
}

func RunCommand(cmd string) (string, error) {
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	var proc *exec.Cmd
	if runtime.GOOS == "windows" {
		proc = exec.Command("cmd", "/C", cmd)
	} else {
		proc = exec.Command("bash", "-c", cmd)
	}

	proc.Stderr = &stderr
	proc.Stdout = &stdout
	err := proc.Run()
	if err != nil {
		return stderr.String(), err
	}

	return stdout.String(), nil
}

func SendOutputAsFile(b *gotgbot.Bot, ctx *ext.Context, code, output string, msg *gotgbot.Message, codeType string) error {
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("%s_output*.txt", codeType))
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(output)
	if err != nil {
		return err
	}

	tmpFile.Seek(0, 0)
	file, _ := os.Open(tmpFile.Name())
	_, err = b.SendDocument(ctx.EffectiveChat.Id, gotgbot.InputFileByReader(fmt.Sprintf("%s.txt", codeType), file), &gotgbot.SendDocumentOpts{
		Caption:   fmt.Sprintf("Output of your %s:\n<pre>%s</pre>", codeType, html.EscapeString(code)),
		ParseMode: "HTML",
	})
	msg.Delete(b, nil)
	return err
}
