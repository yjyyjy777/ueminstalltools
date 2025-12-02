#!/bin/bash

# ==========================================
# CNCY 综合部署工具 - 一键编译脚本
# 包含：Windows 客户端 + Linux服务端(x86/ARM)
# ==========================================

# 定义颜色
GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 输出文件名配置
WIN_OUT="CNCY_Deploy_Tool_v3.6.exe"
AGENT_AMD64="cncyagent_amd64"
AGENT_ARM64="cncyagent_arm64"

# 源码路径
AGENT_SRC="./agent"  # 假设 agent 源码在这个目录下
WIN_SRC="main.go"    # Windows 源码在根目录

echo -e "${CYAN}==========================================${NC}"
echo -e "${CYAN}      🚀 开始构建 CNCY 部署项目${NC}"
echo -e "${CYAN}==========================================${NC}"

# 0. 整理依赖
echo -e "\n[0/3] 正在整理依赖 (go mod tidy)..."
go mod tidy
if [ $? -ne 0 ]; then
    echo -e "${RED} -> 依赖整理失败，请检查代码引用。${NC}"
    exit 1
fi

# ---------------------------------------------------------
# 1. 编译 Linux Agent (x86_64 / AMD64)
# ---------------------------------------------------------
echo -e "\n[1/3] 正在编译 Linux Agent (AMD64)..."
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=amd64

if go build -ldflags "-s -w" -o $AGENT_AMD64 $AGENT_SRC; then
    echo -e "${GREEN} -> 成功: $AGENT_AMD64${NC}"
else
    echo -e "${RED} -> 失败: Agent (AMD64) 编译出错${NC}"
    exit 1
fi

# ---------------------------------------------------------
# 2. 编译 Linux Agent (ARM64 / 麒麟 / 鲲鹏)
# ---------------------------------------------------------
echo -e "\n[2/3] 正在编译 Linux Agent (ARM64)..."
export GOARCH=arm64

if go build -ldflags "-s -w" -o $AGENT_ARM64 $AGENT_SRC; then
    echo -e "${GREEN} -> 成功: $AGENT_ARM64${NC}"
else
    echo -e "${RED} -> 失败: Agent (ARM64) 编译出错${NC}"
    exit 1
fi

# ---------------------------------------------------------
# 3. 编译 Windows GUI 工具
# ---------------------------------------------------------
echo -e "\n[3/3] 正在编译 Windows GUI 工具..."

# 检查 GCC (Fyne GUI 必须)
if ! command -v gcc &> /dev/null; then
    echo -e "${RED} -> 错误: 未找到 gcc。${NC}"
    echo -e "${YELLOW}    提示: 编译 Fyne 需要 TDM-GCC。${NC}"
    exit 1
fi

export CGO_ENABLED=1
export GOOS=windows
export GOARCH=amd64

# -H=windowsgui 隐藏黑框
if go build -ldflags "-s -w -H=windowsgui" -o $WIN_OUT $WIN_SRC; then
    echo -e "${GREEN} -> 成功: $WIN_OUT${NC}"
else
    echo -e "${RED} -> 失败: Windows GUI 编译出错${NC}"
    exit 1
fi

# ---------------------------------------------------------
# 结束
# ---------------------------------------------------------
echo -e "\n${CYAN}==========================================${NC}"
echo -e "${GREEN}🎉 全部构建完成！文件列表：${NC}"
ls -lh $AGENT_AMD64 $AGENT_ARM64 $WIN_OUT
echo -e "${CYAN}==========================================${NC}"

# 防止窗口秒关
read -p "按回车键退出..."