package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// 全局状态
var (
	sshClient        *ssh.Client
	localListener    net.Listener
	isRunning        bool
	currentLocalPort string
)

// 配置常量
const (
	// LocalAgentAMD64 是本地预编译好的 AMD64 架构的 agent 文件
	LocalAgentAMD64 = "cncyagent_amd64"
	// LocalAgentARM64 是本地预编译好的 ARM64 架构的 agent 文件
	LocalAgentARM64 = "cncyagent_arm64"
	// RemotePath 是 agent 在远程服务器上的统一路径
	RemotePath = "/root/cncyagent"
	// RemoteLog 是 agent 在远程服务器上的日志文件路径
	RemoteLog = "/root/agent.log"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("智能部署工具")
	myWindow.Resize(fyne.NewSize(550, 500))

	// --- UI 组件定义 ---
	ipEntry := widget.NewEntry()
	ipEntry.SetPlaceHolder("172.16.10.151")
	passEntry := widget.NewPasswordEntry()
	passEntry.SetPlaceHolder("请输入密码")
	userEntry := widget.NewEntry()
	userEntry.SetText("root")
	sshPortEntry := widget.NewEntry()
	sshPortEntry.SetText("22")

	sshForm := container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("IP 地址", ipEntry),
			widget.NewFormItem("密码", passEntry),
		),
		container.NewGridWithColumns(2,
			widget.NewFormItem("用户", userEntry).Widget,
			widget.NewFormItem("端口", sshPortEntry).Widget,
		),
	)
	sshCard := widget.NewCard("SSH 服务器连接", "", container.NewPadded(sshForm))

	localViewEntry := widget.NewEntry()
	localViewEntry.SetText("9999")
	remoteAppEntry := widget.NewEntry()
	remoteAppEntry.SetText("9898")

	portCard := widget.NewCard("端口隧道配置", "", container.NewPadded(
		container.NewGridWithColumns(2,
			container.NewVBox(widget.NewLabelWithStyle("本地访问端口 (Local)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), localViewEntry),
			container.NewVBox(widget.NewLabelWithStyle("远端监听端口 (Remote)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), remoteAppEntry),
		),
	))

	statusLabel := widget.NewLabel("准备就绪...")
	statusLabel.Wrapping = fyne.TextWrapWord
	statusLabel.Alignment = fyne.TextAlignCenter
	progressBar := widget.NewProgressBarInfinite()
	progressBar.Hide()
	statusCard := widget.NewCard("", "", container.NewVBox(progressBar, statusLabel))

	var btnStart, btnStop, btnBrowser *widget.Button

	logUI := func(msg string) {
		statusLabel.SetText(msg)
		statusLabel.Refresh()
	}

	// 启用/禁用所有输入控件
	setInputsDisabled := func(disabled bool) {
		if disabled {
			ipEntry.Disable()
			passEntry.Disable()
			userEntry.Disable()
			sshPortEntry.Disable()
			localViewEntry.Disable()
			remoteAppEntry.Disable()
		} else {
			ipEntry.Enable()
			passEntry.Enable()
			userEntry.Enable()
			sshPortEntry.Enable()
			localViewEntry.Enable()
			remoteAppEntry.Enable()
		}
	}

	btnStart = widget.NewButtonWithIcon("智能部署 (Start)", theme.MediaPlayIcon(), func() {
		ip, port, user, pass := ipEntry.Text, sshPortEntry.Text, userEntry.Text, passEntry.Text
		lPort, rPort := localViewEntry.Text, remoteAppEntry.Text

		if ip == "" || pass == "" {
			dialog.ShowError(fmt.Errorf("请填写 IP 地址和密码"), myWindow)
			return
		}
		if _, err := os.Stat(LocalAgentAMD64); os.IsNotExist(err) {
			dialog.ShowError(fmt.Errorf("缺失文件: %s", LocalAgentAMD64), myWindow)
			return
		}
		if _, err := os.Stat(LocalAgentARM64); os.IsNotExist(err) {
			dialog.ShowError(fmt.Errorf("缺失文件: %s", LocalAgentARM64), myWindow)
			return
		}

		setInputsDisabled(true)
		btnStart.Disable()
		progressBar.Show()
		logUI("🚀 正在连接服务器...")

		go func() {
			err := runDeployProcess(ip, port, user, pass, lPort, rPort, logUI)

			// 直接在goroutine中更新UI，以兼容旧版Fyne
			progressBar.Hide()
			if err != nil {
				setInputsDisabled(false)
				btnStart.Enable()
				logUI("❌ " + err.Error())
				dialog.ShowError(err, myWindow)
			} else {
				isRunning = true
				currentLocalPort = lPort
				btnStop.Enable()
				btnBrowser.Enable()
				logUI(fmt.Sprintf("✅ 运行中 | 本地: %s <-> 远端: %s", lPort, rPort))
				openBrowser("http://localhost:" + lPort)
			}
		}()
	})
	btnStart.Importance = widget.HighImportance

	btnBrowser = widget.NewButtonWithIcon("打开浏览器", theme.HomeIcon(), func() {
		if currentLocalPort != "" {
			openBrowser("http://localhost:" + currentLocalPort)
		}
	})

	btnStop = widget.NewButtonWithIcon("停止 (Stop)", theme.MediaStopIcon(), func() {
		logUI("正在断开连接...")
		go func() {
			if sshClient != nil {
				s, err := sshClient.NewSession()
				if err == nil {
					_ = s.Run("pkill -f cncyagent")
					_ = s.Close()
				}
				_ = sshClient.Close()
			}
			if localListener != nil {
				_ = localListener.Close()
			}
			isRunning = false

			// 直接在goroutine中更新UI，以兼容旧版Fyne
			setInputsDisabled(false)
			btnStart.Enable()
			btnStop.Disable()
			btnBrowser.Disable()
			logUI("👋 已停止")
		}()
	})

	// 设置初始UI状态
	btnStart.Enable()
	btnStop.Disable()
	btnBrowser.Disable()

	btnGroup := container.NewGridWithColumns(3, btnStart, btnBrowser, btnStop)
	mainLayout := container.NewVBox(sshCard, portCard, statusCard, layout.NewSpacer(), btnGroup)
	myWindow.SetContent(container.NewPadded(mainLayout))

	myWindow.SetCloseIntercept(func() {
		if isRunning {
			dialog.ShowConfirm("退出", "服务正在运行，确认退出？", func(b bool) {
				if b {
					btnStop.OnTapped()
					time.Sleep(500 * time.Millisecond)
					myWindow.Close()
				}
			}, myWindow)
		} else {
			myWindow.Close()
		}
	})

	myWindow.ShowAndRun()
}

// 核心逻辑
func runDeployProcess(host, port, user, pass, localPort, remotePort string, logFunc func(string)) error {
	// 1. 连接
	config := &ssh.ClientConfig{
		User: user, Auth: []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%s", host, port), config)
	if err != nil {
		return err
	}
	sshClient = client

	// 2. 检测架构
	logFunc("🔍 检测架构...")
	sessArch, _ := client.NewSession()
	outArch, err := sessArch.Output("uname -m")
	_ = sessArch.Close()
	if err != nil {
		return fmt.Errorf("架构检测失败: %v", err)
	}

	arch := strings.TrimSpace(string(outArch))
	var localFile string
	if arch == "x86_64" {
		localFile = LocalAgentAMD64
		logFunc("识别为 x86_64")
	} else if arch == "aarch64" {
		localFile = LocalAgentARM64
		logFunc("识别为 ARM64")
	} else {
		return fmt.Errorf("不支持架构: %s", arch)
	}

	// 3. 清理与上传
	logFunc("🧹 清理环境...")
	sessClean, _ := client.NewSession()
	_ = sessClean.Run(fmt.Sprintf("pkill -f cncyagent; rm -f %s", RemotePath))
	_ = sessClean.Close()
	time.Sleep(500 * time.Millisecond)

	logFunc("📤 上传组件...")
	if err := uploadFile(client, localFile, RemotePath); err != nil {
		return err
	}

	// 4. 启动
	logFunc("⚙️ 启动服务...")
	startCmd := fmt.Sprintf("setenforce 0 || true; chmod +x %s; nohup %s -port %s > %s 2>&1 < /dev/null &", RemotePath, RemotePath, remotePort, RemoteLog)
	sessStart, _ := client.NewSession()
	err = sessStart.Start(startCmd)
	_ = sessStart.Close()
	if err != nil {
		return fmt.Errorf("启动远程服务失败: %v", err)
	}
	time.Sleep(1 * time.Second)

	// 5. 建立隧道
	logFunc(fmt.Sprintf("🔗 建立隧道 %s -> %s...", localPort, remotePort))
	listener, err := net.Listen("tcp", "localhost:"+localPort)
	if err != nil {
		return fmt.Errorf("本地端口占用")
	}
	localListener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				rConn, err := client.Dial("tcp", "127.0.0.1:"+remotePort)
				if err != nil {
					return
				}
				defer func() { _ = rConn.Close() }()
				go func() { _, _ = io.Copy(rConn, c) }()
				_, _ = io.Copy(c, rConn)
			}(conn)
		}
	}()
	return nil
}

func uploadFile(client *ssh.Client, local, remote string) error {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer func() { _ = sftpClient.Close() }()

	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	dst, err := sftpClient.Create(remote)
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return sftpClient.Chmod(remote, 0777)
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	if err := exec.Command(cmd, args...).Start(); err != nil {
		log.Printf("无法打开浏览器: %v", err)
	}
}
