#!/bin/bash

# HubProxy 一键安装脚本
# 支持自动下载最新版本或使用本地文件安装
set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
REPO="sky22333/hubproxy"
GITHUB_API="https://api.github.com/repos/${REPO}"
GITHUB_RELEASES="${GITHUB_API}/releases"
SERVICE_NAME="hubproxy"
INSTALL_DIR="/opt/hubproxy"
CONFIG_FILE="config.toml"
BINARY_NAME="hubproxy"
LOG_DIR="/var/log/hubproxy"
TEMP_DIR="/tmp/hubproxy-install"

echo -e "${BLUE}HubProxy 一键安装脚本${NC}"
echo "================================================="

# 检查是否以root权限运行
if [[ $EUID -ne 0 ]]; then
           echo -e "${RED}此脚本需要root权限运行${NC}"
   echo "请使用: sudo $0"
   exit 1
fi

# 检测系统架构
detect_arch() {
    local arch=$(uname -m)
    case $arch in
        x86_64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            echo -e "${RED}不支持的架构: $arch${NC}"
            exit 1
            ;;
    esac
}

ARCH=$(detect_arch)
echo -e "${BLUE}检测到架构: linux-${ARCH}${NC}"

# 检查是否为本地安装模式
if [ -f "${BINARY_NAME}" ]; then
    echo -e "${BLUE}发现本地文件，使用本地安装模式${NC}"
    LOCAL_INSTALL=true
else
    echo -e "${BLUE}本地无文件，使用自动下载模式${NC}"
    LOCAL_INSTALL=false
    
    # 检查依赖
    missing_deps=()
    for cmd in curl jq tar skopeo; do
        if ! command -v $cmd &> /dev/null; then
            missing_deps+=($cmd)
        fi
    done
    
    if [ ${#missing_deps[@]} -gt 0 ]; then
        echo -e "${YELLOW}检测到缺少依赖: ${missing_deps[*]}${NC}"
        echo -e "${BLUE}正在自动安装依赖...${NC}"
        
        apt update && apt install -y curl jq skopeo
        if [ $? -ne 0 ]; then
            echo -e "${RED}依赖安装失败${NC}"
            exit 1
        fi
        
        # 重新检查依赖
        for cmd in curl jq tar skopeo; do
            if ! command -v $cmd &> /dev/null; then
                echo -e "${RED}依赖安装后仍缺少: $cmd${NC}"
                exit 1
            fi
        done
        
        echo -e "${GREEN}依赖安装成功${NC}"
    fi
fi

# 自动下载功能
if [ "$LOCAL_INSTALL" = false ]; then
    echo -e "${BLUE}获取最新版本信息...${NC}"
    LATEST_RELEASE=$(curl -s "${GITHUB_RELEASES}/latest")
    if [ $? -ne 0 ]; then
        echo -e "${RED}无法获取版本信息${NC}"
        exit 1
    fi

    VERSION=$(echo "$LATEST_RELEASE" | jq -r '.tag_name')
    if [ "$VERSION" = "null" ]; then
        echo -e "${RED}无法解析版本信息${NC}"
        exit 1
    fi

    echo -e "${GREEN}最新版本: ${VERSION}${NC}"

    # 构造下载URL
    ASSET_NAME="hubproxy-${VERSION}-linux-${ARCH}.tar.gz"
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET_NAME}"

    echo -e "${BLUE}下载: ${ASSET_NAME}${NC}"

    # 创建临时目录并下载
    rm -rf "${TEMP_DIR}"
    mkdir -p "${TEMP_DIR}"
    cd "${TEMP_DIR}"

    curl -L -o "${ASSET_NAME}" "${DOWNLOAD_URL}"
    if [ $? -ne 0 ]; then
        echo -e "${RED}下载失败${NC}"
        exit 1
    fi

    # 解压
    tar -xzf "${ASSET_NAME}"
    if [ $? -ne 0 ] || [ ! -d "hubproxy" ]; then
        echo -e "${RED}解压失败${NC}"
        exit 1
    fi

    cd hubproxy
    echo -e "${GREEN}下载完成${NC}"
fi

echo -e "${YELLOW}开始安装 HubProxy...${NC}"

# 停止现有服务（如果存在）
if systemctl is-active --quiet ${SERVICE_NAME} 2>/dev/null; then
    echo -e "${YELLOW}停止现有服务...${NC}"
    systemctl stop ${SERVICE_NAME}
fi

# 备份现有配置（如果存在）
CONFIG_BACKUP_EXISTS=false
if [ -f "${INSTALL_DIR}/${CONFIG_FILE}" ]; then
    echo -e "${BLUE}备份现有配置...${NC}"
    cp "${INSTALL_DIR}/${CONFIG_FILE}" "${TEMP_DIR}/config.toml.backup"
    CONFIG_BACKUP_EXISTS=true
fi

# 1. 创建目录结构
echo -e "${BLUE}创建目录结构${NC}"
mkdir -p ${INSTALL_DIR}
mkdir -p ${LOG_DIR}
chmod 755 ${INSTALL_DIR}
chmod 755 ${LOG_DIR}

# 2. 复制二进制文件
echo -e "${BLUE}复制二进制文件${NC}"
cp "${BINARY_NAME}" "${INSTALL_DIR}/"
chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

# 3. 复制配置文件
echo -e "${BLUE}复制配置文件${NC}"
if [ -f "${CONFIG_FILE}" ]; then
    if [ "$CONFIG_BACKUP_EXISTS" = false ]; then
        cp "${CONFIG_FILE}" "${INSTALL_DIR}/"
        echo -e "${GREEN}配置文件复制成功${NC}"
    else
        echo -e "${YELLOW}保留现有配置文件${NC}"
    fi
else
    echo -e "${YELLOW}配置文件不存在，将使用默认配置${NC}"
fi

# 4. 前端文件已嵌入二进制程序，无需复制

# 5. 安装systemd服务文件
echo -e "${BLUE}安装systemd服务文件${NC}"
cp "${SERVICE_NAME}.service" "/etc/systemd/system/"
systemctl daemon-reload

# 6. 恢复配置文件（如果有备份）
if [ "$CONFIG_BACKUP_EXISTS" = true ]; then
    echo -e "${BLUE}恢复配置文件...${NC}"
    cp "${TEMP_DIR}/config.toml.backup" "${INSTALL_DIR}/${CONFIG_FILE}"
fi

# 7. 启用并启动服务
echo -e "${BLUE}启用并启动服务${NC}"
systemctl enable ${SERVICE_NAME}
systemctl start ${SERVICE_NAME}

# 8. 清理临时文件
if [ "$LOCAL_INSTALL" = false ]; then
    echo -e "${BLUE}清理临时文件...${NC}"
    cd /
    rm -rf "${TEMP_DIR}"
fi

# 9. 检查服务状态
sleep 2
if systemctl is-active --quiet ${SERVICE_NAME}; then
    echo ""
    echo -e "${GREEN}HubProxy 安装成功！${NC}"
    echo -e "${GREEN}默认运行端口: 5000${NC}"
    echo -e "${GREEN}配置文件路径: ${INSTALL_DIR}/${CONFIG_FILE}${NC}"
else
    echo -e "${RED}服务启动失败${NC}"
    echo "查看错误日志: sudo journalctl -u ${SERVICE_NAME} -f"
    exit 1
fi 