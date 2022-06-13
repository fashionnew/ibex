//go:build windows
// +build windows

package timer

import (
	"bytes"
	"context"
	"fmt"
	"github.com/toolkits/pkg/file"
	"github.com/toolkits/pkg/runner"
	"log"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ulricqin/ibex/src/agentd/client"
	"github.com/ulricqin/ibex/src/agentd/config"
)

type Task struct {
	sync.Mutex

	Id     int64
	Clock  int64
	Action string
	Status string

	alive  bool
	Cmd    *exec.Cmd
	Stdout bytes.Buffer
	Stderr bytes.Buffer

	Args    string
	Account string
	Timeout int
}

func (t *Task) SetStatus(status string) {
	t.Lock()
	t.Status = status
	t.Unlock()
}

func (t *Task) GetStatus() string {
	t.Lock()
	s := t.Status
	t.Unlock()
	return s
}

func (t *Task) GetAlive() bool {
	t.Lock()
	pa := t.alive
	t.Unlock()
	return pa
}

func (t *Task) SetAlive(pa bool) {
	t.Lock()
	t.alive = pa
	t.Unlock()
}

func (t *Task) GetStdout() string {
	t.Lock()
	out := t.Stdout.String()
	t.Unlock()
	return out
}

func (t *Task) GetStderr() string {
	t.Lock()
	out := t.Stderr.String()
	t.Unlock()
	return out
}

func (t *Task) ResetBuff() {
	t.Lock()
	t.Stdout.Reset()
	t.Stderr.Reset()
	t.Unlock()
}

func (t *Task) doneBefore() bool {
	doneFlag := filepath.Join(config.C.MetaDir, fmt.Sprint(t.Id), fmt.Sprintf("%d.done", t.Clock))
	return file.IsExist(doneFlag)
}

func (t *Task) loadResult() {
	metadir := config.C.MetaDir

	doneFlag := filepath.Join(metadir, fmt.Sprint(t.Id), fmt.Sprintf("%d.done", t.Clock))
	stdoutFile := filepath.Join(metadir, fmt.Sprint(t.Id), "stdout")
	stderrFile := filepath.Join(metadir, fmt.Sprint(t.Id), "stderr")

	var err error

	t.Status, err = file.ReadStringTrim(doneFlag)
	if err != nil {
		log.Printf("E: read file %s fail %v", doneFlag, err)
	}
	stdout, err := file.ReadString(stdoutFile)
	if err != nil {
		log.Printf("E: read file %s fail %v", stdoutFile, err)
	}
	stderr, err := file.ReadString(stderrFile)
	if err != nil {
		log.Printf("E: read file %s fail %v", stderrFile, err)
	}

	t.Stdout = *bytes.NewBufferString(stdout)
	t.Stderr = *bytes.NewBufferString(stderr)
}

func (t *Task) prepare() error {
	if t.Account != "" {
		// already prepared
		return nil
	}

	IdDir := filepath.Join(config.C.MetaDir, fmt.Sprint(t.Id))
	err := file.EnsureDir(IdDir)
	if err != nil {
		log.Printf("E: mkdir -p %s fail: %v", IdDir, err)
		return err
	}

	writeFlag := filepath.Join(IdDir, ".write")
	if file.IsExist(writeFlag) {
		// 从磁盘读取
		argsFile := filepath.Join(IdDir, "args")
		args, err := file.ReadStringTrim(argsFile)
		if err != nil {
			log.Printf("E: read %s fail %v", argsFile, err)
			return err
		}

		accountFile := filepath.Join(IdDir, "account")
		account, err := file.ReadStringTrim(accountFile)
		if err != nil {
			log.Printf("E: read %s fail %v", accountFile, err)
			return err
		}

		t.Args = args
		t.Account = account
	} else {
		// 从远端读取，再写入磁盘
		script, args, account, timeout, err := client.Meta(t.Id)
		if err != nil {
			log.Println("E: query task meta fail:", err)
			return err
		}

		scriptFile := filepath.Join(IdDir, "script.bat")
		_, err = file.WriteString(scriptFile, script)
		if err != nil {
			log.Printf("E: write script to %s fail: %v", scriptFile, err)
			return err
		}

		argsFile := filepath.Join(IdDir, "args")
		_, err = file.WriteString(argsFile, args)
		if err != nil {
			log.Printf("E: write args to %s fail: %v", argsFile, err)
			return err
		}

		accountFile := filepath.Join(IdDir, "account")
		_, err = file.WriteString(accountFile, account)
		if err != nil {
			log.Printf("E: write account to %s fail: %v", accountFile, err)
			return err
		}

		_, err = file.WriteString(writeFlag, "")
		if err != nil {
			log.Printf("E: create %s flag file fail: %v", writeFlag, err)
			return err
		}

		t.Args = args
		t.Account = account
		t.Timeout = timeout
	}

	return nil
}

func (t *Task) start() {
	if t.GetAlive() {
		return
	}

	err := t.prepare()
	if err != nil {
		return
	}

	args := t.Args
	if args != "" {
		args = strings.Replace(args, ",,", "' '", -1)
		args = "'" + args + "'"
	}

	scriptFile := filepath.Join(config.C.MetaDir, fmt.Sprint(t.Id), "script.bat")
	log.Println("%s %s %s", config.C.MetaDir, fmt.Sprint(t.Id), args)
	if !filepath.IsAbs(scriptFile) {
		scriptFile = filepath.Join(runner.Cwd, scriptFile)
	}

	sh := fmt.Sprintf("%s %s", scriptFile, args)
	var cmd *exec.Cmd

	loginUser, err := user.Current()

	// current login user not administrator
	log.Println("E: get current bat:", sh)

	cmd = exec.Command(sh)
	cmd.Dir = loginUser.HomeDir
	log.Println("E: get current loginUser.HomeDir:", loginUser.HomeDir)

	cmd.Stdout = &t.Stdout
	cmd.Stderr = &t.Stderr
	t.Cmd = cmd

	err = CmdStart(cmd)
	if err != nil {
		log.Printf("E: cannot start cmd of task[%d]: %v", t.Id, err)
		return
	}

	go runProcess(t)
}

func (t *Task) kill() {
	go killProcess(t)
}

func runProcess(t *Task) {
	t.SetAlive(true)
	defer t.SetAlive(false)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(t.Timeout*2)*time.Second)
	defer cancel()

	waitChan := make(chan struct{}, 1)
	defer close(waitChan)

	// 超时杀掉进程组 或正常退出
	go func() {
		select {
		case <-ctx.Done():
			log.Printf("E: timeout kill task [%d] pid:%d", t.Id, t.Cmd.Process.Pid)
			CmdKill(t.Cmd)
		case <-waitChan:
		}
	}()

	err := t.Cmd.Wait()

	if err != nil {
		if strings.Contains(err.Error(), "exit status ") {
			t.SetStatus("killed")
			log.Printf("E: process of task[%d] killed", t.Id)
		} else {
			t.SetStatus("failed")
			log.Printf("E: process of task[%d] return error: %v", t.Id, err)
		}
	} else {
		t.SetStatus("success")
		log.Printf("D: process of task[%d] done", t.Id)
	}

	persistResult(t)
}

func persistResult(t *Task) {
	metadir := config.C.MetaDir

	stdout := filepath.Join(metadir, fmt.Sprint(t.Id), "stdout")
	stderr := filepath.Join(metadir, fmt.Sprint(t.Id), "stderr")
	doneFlag := filepath.Join(metadir, fmt.Sprint(t.Id), fmt.Sprintf("%d.done", t.Clock))

	file.WriteString(stdout, t.GetStdout())
	file.WriteString(stderr, t.GetStderr())
	file.WriteString(doneFlag, t.GetStatus())
}

func killProcess(t *Task) {
	t.SetAlive(true)
	defer t.SetAlive(false)

	log.Printf("D: begin kill process of task[%d]", t.Id)

	err := CmdKill(t.Cmd)
	if err != nil {
		t.SetStatus("killfailed")
		log.Printf("D: kill process of task[%d] fail: %v", t.Id, err)
	} else {
		t.SetStatus("killed")
		log.Printf("D: process of task[%d] killed", t.Id)
	}

	persistResult(t)
}
