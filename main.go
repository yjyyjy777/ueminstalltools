package main

import (
	"fmt"
	"io"
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
	// 本地预编译好的两个文件
	LocalAgentAMD64 = "cncyagent_amd64"
	LocalAgentARM64 = "cncyagent_arm64"
	// 远程统一路径
	RemotePath = "/root/cncyagent"
	RemoteLog  = "/root/agent.log"
)

func main() {
	myApp := app.New()
	myApp.Settings().SetTheme(theme.LightTheme())

	myWindow := myApp.NewWindow("智能部署工具")
	myWindow.Resize(fyne.NewSize(550, 500))

	// --- 2. SSH 输入 ---
	ipEntry := widget.NewEntry()
	ipEntry.SetPlaceHolder("192.168.x.x")
	sshPortEntry := widget.NewEntry()
	sshPortEntry.SetText("22")
	userEntry := widget.NewEntry()
	userEntry.SetText("root")
	passEntry := widget.NewPasswordEntry()
	passEntry.SetPlaceHolder("Password")

	sshForm := container.NewGridWithColumns(1,
		widget.NewFormItem("IP 地址", ipEntry).Widget,
		container.NewGridWithColumns(2,
			widget.NewFormItem("端口", sshPortEntry).Widget,
			widget.NewFormItem("用户", userEntry).Widget,
		),
		widget.NewFormItem("密码", passEntry).Widget,
	)
	sshCard := widget.NewCard("SSH 服务器连接", "", container.NewPadded(sshForm))

	// --- 3. 端口配置 ---
	localViewEntry := widget.NewEntry()
	localViewEntry.SetText("9999")
	lblLocal := widget.NewLabel("本地访问端口 (Local)")
	lblLocal.TextStyle = fyne.TextStyle{Bold: true}

	remoteAppEntry := widget.NewEntry()
	remoteAppEntry.SetText("9898") // 默认 9898
	lblRemote := widget.NewLabel("远端监听端口 (Remote)")
	lblRemote.TextStyle = fyne.TextStyle{Bold: true}

	portGrid := container.NewGridWithColumns(2,
		container.NewVBox(lblLocal, localViewEntry),
		container.NewVBox(lblRemote, remoteAppEntry),
	)
	portCard := widget.NewCard("端口隧道配置", "", container.NewPadded(portGrid))

	// --- 4. 状态日志 ---
	statusLabel := widget.NewLabel("准备就绪...")
	statusLabel.Wrapping = fyne.TextWrapWord
	statusLabel.Alignment = fyne.TextAlignCenter
	progressBar := widget.NewProgressBarInfinite()
	progressBar.Hide()
	statusCard := widget.NewCard("", "", container.NewVBox(progressBar, statusLabel))

	// --- 5. 按钮 ---
	var btnStart, btnStop, btnBrowser *widget.Button
	logUI := func(msg string) { statusLabel.SetText(msg); statusLabel.Refresh() }

	// [启动]
	btnStart = widget.NewButtonWithIcon("智能部署 (Start)", theme.MediaPlayIcon(), func() {
		if isRunning {
			return
		}
		ip, port, user, pass := ipEntry.Text, sshPortEntry.Text, userEntry.Text, passEntry.Text
		lPort, rPort := localViewEntry.Text, remoteAppEntry.Text

		if ip == "" || pass == "" {
			dialog.ShowError(fmt.Errorf("请填写完整信息"), myWindow)
			return
		}
		// 检查本地文件
		if _, err := os.Stat(LocalAgentAMD64); os.IsNotExist(err) {
			dialog.ShowError(fmt.Errorf("缺失文件: %s", LocalAgentAMD64), myWindow)
			return
		}
		if _, err := os.Stat(LocalAgentARM64); os.IsNotExist(err) {
			dialog.ShowError(fmt.Errorf("缺失文件: %s", LocalAgentARM64), myWindow)
			return
		}

		btnStart.Disable()
		ipEntry.Disable()
		sshPortEntry.Disable()
		userEntry.Disable()
		passEntry.Disable()
		localViewEntry.Disable()
		remoteAppEntry.Disable()
		progressBar.Show()
		logUI("🚀 正在连接服务器...")

		go func() {
			err := runDeployProcess(ip, port, user, pass, lPort, rPort, logUI)
			if err != nil {
				progressBar.Hide()
				btnStart.Enable()
				ipEntry.Enable()
				sshPortEntry.Enable()
				userEntry.Enable()
				passEntry.Enable()
				localViewEntry.Enable()
				remoteAppEntry.Enable()
				logUI("❌ " + err.Error())
				dialog.ShowError(err, myWindow)
			} else {
				progressBar.Hide()
				isRunning = true
				currentLocalPort = lPort
				btnStop.Enable()
				btnBrowser.Enable()
				logUI(fmt.Sprintf("✅ 运行中 | 本地: %s <-> 远端: %s", lPort, rPort))
			}
		}()
	})
	btnStart.Importance = widget.HighImportance

	// [浏览器]
	btnBrowser = widget.NewButtonWithIcon("打开浏览器", theme.HomeIcon(), func() {
		if currentLocalPort != "" {
			openBrowser("http://localhost:" + currentLocalPort)
		}
	})
	btnBrowser.Disable()

	// [停止]
	btnStop = widget.NewButtonWithIcon("停止 (Stop)", theme.MediaStopIcon(), func() {
		if !isRunning {
			return
		}
		logUI("正在断开连接...")
		go func() {
			if sshClient != nil {
				s, _ := sshClient.NewSession()
				// 远程文件名固定为 cncyagent
				s.Run("pkill -f cncyagent")
				s.Close()
				sshClient.Close()
			}
			if localListener != nil {
				localListener.Close()
			}
			isRunning = false

			btnStop.Disable()
			btnBrowser.Disable()
			btnStart.Enable()

			ipEntry.Enable()
			sshPortEntry.Enable()
			userEntry.Enable()
			passEntry.Enable()
			localViewEntry.Enable()
			remoteAppEntry.Enable()
			logUI("👋 已停止")
		}()
	})
	btnStop.Disable()

	// 布局
	btnGroup := container.NewGridWithColumns(3, btnStart, btnBrowser, btnStop)
	mainLayout := container.NewVBox(
		sshCard,
		portCard,
		statusCard,
		layout.NewSpacer(),
		btnGroup,
	)
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
	sessArch.Close()
	if err != nil {
		return fmt.Errorf("架构检测失败: %v", err)
	}

	arch := strings.TrimSpace(string(outArch))
	localFile := ""
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
	sessClean.Run(fmt.Sprintf("pkill -f cncyagent; rm -f %s", RemotePath))
	sessClean.Close()
	time.Sleep(500 * time.Millisecond)

	logFunc("📤 上传组件...")
	if err := uploadFile(client, localFile, RemotePath); err != nil {
		return err
	}

	// 4. 启动
	logFunc("⚙️ 启动服务...")
	startCmd := fmt.Sprintf("setenforce 0 || true; chmod +x %s; nohup %s -port %s > %s 2>&1 < /dev/null &", RemotePath, RemotePath, remotePort, RemoteLog)
	sessStart, _ := client.NewSession()
	sessStart.Start(startCmd)
	sessStart.Close()
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
				defer c.Close()
				// 【关键修复】使用 127.0.0.1 而不是 localhost，解决 ARM/IPv6 问题
				rConn, err := client.Dial("tcp", "127.0.0.1:"+remotePort)
				if err != nil {
					return
				}
				defer rConn.Close()
				go io.Copy(rConn, c)
				io.Copy(c, rConn)
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
	defer sftpClient.Close()
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := sftpClient.Create(remote)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	dst.Close()
	return sftpClient.Chmod(remote, 0777)
}

func openBrowser(url string) {
	var cmd string
	var args []string
	if runtime.GOOS == "windows" {
		cmd = "cmd"
		args = []string{"/c", "start"}
	} else {
		cmd = "xdg-open"
	}
	args = append(args, url)
	exec.Command(cmd, args...).Start()
}
