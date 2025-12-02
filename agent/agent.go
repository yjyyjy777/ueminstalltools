package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ================= 1. 配置区域 =================
var (
	ServerPort      string
	UploadTargetDir = "/root"
	RpmCacheDir     = "/root/rpm_cache"
	InstallWorkDir  = "/root/install-cncy"
	InstallScript   = "install.sh"
	UpdateScript    = "mdm.sh"
	IsoSavePath     = "/root/os.iso"
	IsoMountPoint   = "/mnt/cdrom"
	RepoBackupDir   = "/etc/yum.repos.d/backup_cncy"

	MinioEndpoint = "127.0.0.1:9000"
	MinioUser     = "admin"
	MinioPass     = "Nqsky1130"
	MinioBucket   = "nqsky"
)

var uemServices = []string{
	"tomcat", "Platform_java", "licserver", "AppServer", "EMMBackend",
	"nginx", "redis", "mysqld", "minio", "rabbitmq-server", "scep-go",
}

var logFileMap = map[string]string{
	"tomcat":       "/opt/emm/current/tomcat/logs/catalina.out",
	"nginx_access": "/var/log/nginx/access.log",
	"nginx_error":  "/var/log/nginx/error.log",
	"app_server":   "/emm/logs/AppServer/appServer.log",
	"emm_backend":  "/emm/logs/emm_backend/emmBackend.log",
	"license":      "/emm/logs/licenseServer/licenseServer.log",
	"platform":     "/emm/logs/platform/platform.log",
}

// ================= 2. 前端页面 =================
const htmlPage = `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>综合运维平台</title>
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.min.css" />
    <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.4.0/css/all.min.css">
    <style>
        body { font-family: 'Segoe UI', sans-serif; background: #2c3e50; margin: 0; height: 100vh; display: flex; flex-direction: column; overflow: hidden; }
        .navbar { background: #34495e; padding: 0 20px; height: 50px; display: flex; align-items: center; border-bottom: 1px solid #1abc9c; }
        .brand { color: #fff; font-weight: bold; font-size: 18px; margin-right: 20px; }
        .tab-btn { background: transparent; border: none; color: #bdc3c7; font-size: 13px; padding: 0 10px; height: 100%; cursor: pointer; transition: 0.3s; border-bottom: 3px solid transparent; }
        .tab-btn:hover { color: white; background: rgba(255,255,255,0.05); }
        .tab-btn.active { color: #1abc9c; border-bottom: 3px solid #1abc9c; background: rgba(26, 188, 156, 0.1); }
        .content { flex: 1; position: relative; background: #ecf0f1; overflow-y: auto; }
        .panel { display: none; width: 100%; min-height: 100%; padding: 20px; box-sizing: border-box; }
        .panel.active { display: block; }
        .container-box { padding: 20px; max-width: 1000px; margin: 0 auto; width: 100%; box-sizing: border-box; display: flex; flex-direction: column; height: 100%; }
        .card { background: white; padding: 15px; border-radius: 6px; box-shadow: 0 2px 5px rgba(0,0,0,0.1); margin-bottom: 15px; display: flex; flex-direction: column; }
        h3 { margin-top: 0; border-bottom: 2px solid #eee; padding-bottom: 10px; color: #2c3e50; display: flex; justify-content: space-between; align-items: center; font-size: 16px; }
        .term-box { flex: 1; background: #1e1e1e; padding: 10px; overflow-y: auto; border-radius: 6px; color: #0f0; font-family: Consolas, monospace; font-size: 13px; white-space: pre-wrap; border: 1px solid #333; }
        .full-term { width: 100%; height: 100%; background: #000; padding: 10px; box-sizing: border-box; }
        table { width: 100%; border-collapse: collapse; margin-top: 10px; font-size: 14px; }
        th, td { text-align: left; padding: 8px; border-bottom: 1px solid #eee; }
        th { background-color: #f8f9fa; color: #666; position: sticky; top: 0; }
        .pass { color: #27ae60; font-weight: bold; }
        .fail { color: #c0392b; font-weight: bold; }
        .warn { color: #f39c12; font-weight: bold; }
        .progress-bg { width: 100%; background-color: #e0e0e0; border-radius: 4px; height: 16px; overflow: hidden; position: relative; }
        .progress-bar { height: 100%; text-align: center; line-height: 16px; color: white; font-size: 10px; transition: width 0.5s; }
        .bg-green { background-color: #27ae60; } .bg-orange { background-color: #f39c12; } .bg-red { background-color: #c0392b; }
        .disk-text { font-size: 12px; color: #666; margin-top: 2px; display: flex; justify-content: space-between; }
        .fm-toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; padding-bottom: 10px; border-bottom: 1px solid #eee; }
        .fm-path { flex: 1; padding: 5px; border: 1px solid #ddd; border-radius: 4px; background: #f9f9f9; font-family: monospace; }
        .fm-list { flex: 1; overflow-y: auto; }
        .icon-dir { color: #f39c12; margin-right: 5px; } .icon-file { color: #95a5a6; margin-right: 5px; }
        .link-dir { color: #2980b9; cursor: pointer; text-decoration: none; font-weight: bold; } .link-dir:hover { text-decoration: underline; }
        .log-layout { display: flex; height: 100%; border: 1px solid #ddd; border-radius: 6px; overflow: hidden; background: white; }
        .log-sidebar { width: 240px; background: #f8f9fa; border-right: 1px solid #ddd; display: flex; flex-direction: column; }
        .log-sidebar-header { padding: 10px; background: #e9ecef; font-weight: bold; font-size: 14px; border-bottom: 1px solid #ddd; }
        .log-list { flex: 1; overflow-y: auto; list-style: none; padding: 0; margin: 0; }
        .log-item { padding: 8px 12px; cursor: pointer; font-size: 13px; color: #333; border-bottom: 1px solid #f1f1f1; transition: 0.2s; display: flex; justify-content: space-between; align-items: center; }
        .log-item:hover { background: #e2e6ea; } .log-item.active { background: #3498db; color: white; border-left: 4px solid #2980b9; }
        .log-viewer-container { flex: 1; display: flex; flex-direction: column; background: #1e1e1e; }
        .log-viewer-header { padding: 5px 10px; background: #2c3e50; color: #ecf0f1; font-size: 12px; display: flex; justify-content: space-between; align-items: center; }
        .log-content { flex: 1; overflow-y: auto; padding: 10px; font-family: 'Consolas', monospace; font-size: 12px; color: #dcdcdc; white-space: pre-wrap; word-break: break-all; }
        button { background: #2980b9; color: white; border: none; padding: 6px 12px; border-radius: 4px; cursor: pointer; font-size: 13px; transition: 0.2s; }
        button:hover { background: #3498db; } button:disabled { background: #95a5a6; cursor: not-allowed; }
        .btn-sm { padding: 4px 8px; font-size: 12px; } 
        .btn-fix { background: #e67e22; } .btn-fix:hover { background: #d35400; }
        .btn-green { background: #27ae60; } .btn-green:hover { background: #219150; }
        .btn-orange { background: #e67e22; } .btn-orange:hover { background: #d35400; }
        .btn-red { background: #e74c3c; } .btn-red:hover { background: #c0392b; }
        .btn-restart { background: #e74c3c; } .btn-restart:hover { background: #c0392b; }
        .btn-dl-log { background: transparent; border: 1px solid #ccc; color: #666; padding: 2px 6px; border-radius: 3px; font-size: 11px; cursor: pointer; }
        .btn-dl-log:hover { background: #27ae60; color: white; border-color: #27ae60; }
        input[type="file"], input[type="text"] { border: 1px solid #ccc; padding: 5px; background: white; font-size: 13px; }
        .grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; }
        .about-table td { padding: 10px; }
        .about-table tr:not(:last-child) td { border-bottom: 1px solid #f0f0f0; }
    </style>
</head>
<body>
<div class="navbar">
    <button class="tab-btn active" onclick="switchTab('check')">🔍 系统体检</button>
    <button class="tab-btn" onclick="switchTab('deps')">🔧 环境依赖</button>
    <button class="tab-btn" onclick="switchTab('deploy')">📦 部署/更新</button>
    <button class="tab-btn" onclick="switchTab('files')">📂 文件管理</button>
    <button class="tab-btn" onclick="switchTab('terminal')">💻 终端</button>
    <button class="tab-btn" onclick="switchTab('logs')">📜 日志查看</button>
    <button class="tab-btn" onclick="switchTab('about')">ℹ️ 关于</button>
</div>
<div class="content">
    <div id="panel-check" class="panel active">
        <div class="grid-2">
            <div>
                <div class="card"><h3>🖥️ 基础环境 <button onclick="runCheck()" class="btn-sm"><i class="fas fa-sync"></i> 刷新</button></h3><table id="baseTable"><tbody><tr><td>加载中...</td></tr></tbody></table></div>
                <div class="card"><h3>💾 磁盘空间概览</h3><div id="diskList" style="margin-top:10px;">加载中...</div></div>
                <div class="card"><h3>🛡️ 安全与网络</h3><table id="secTable"><tbody><tr><td>加载中...</td></tr></tbody></table></div>
            </div>
            <div>
                <div class="card"><h3>🚀 UEM 服务监控</h3><div id="uemStatusBox"><p>检测 UEM 安装状态...</p></div></div>
                <div class="card"><h3>🗄️ MinIO 检测</h3><table id="minioTable"><tbody><tr><td>加载中...</td></tr></tbody></table></div>
            </div>
        </div>
    </div>
    
    <div id="panel-deps" class="panel"><div class="container-box">
        <div class="card">
            <h3>💿 ISO 挂载 (配置本地 YUM)</h3>
            <div style="display:flex; flex-direction:column; gap:10px;">
                <div style="display:flex; align-items:center; gap:10px;">
                    <span style="width:80px; color:#666;">上传镜像:</span>
                    <input type="file" id="isoInput" accept=".iso" style="width:300px;">
                    <button onclick="mountIso()">上传并挂载</button>
                </div>
                <div style="display:flex; align-items:center; gap:10px;">
                    <span style="width:80px; color:#666;">本地路径:</span>
                    <input type="text" id="isoPathInput" placeholder="/tmp/kylin.iso" style="width:300px;">
                    <button class="btn-orange" onclick="mountLocalIso()">使用本地文件</button>
                </div>
            </div>
            <div id="yum-log" class="term-box" style="height:120px;margin-top:10px">等待操作...</div>
        </div>
        <div class="card"><h3>🛠️ RPM 安装</h3><div style="display:flex;gap:10px"><input type="file" id="rpmInput" accept=".rpm"><button onclick="installRpm()">执行安装</button></div><div id="rpm-log" class="term-box" style="height:120px;margin-top:10px"></div></div>
    </div></div>
    
    <div id="panel-deploy" class="panel"><div class="container-box"><div class="card"><h3>📦 系统包上传</h3><div style="display:flex;gap:10px"><input type="file" id="fileInput" accept=".tar.gz"><button onclick="uploadFile()">上传解压</button><span id="uploadStatus" style="font-weight:bold"></span></div></div><div class="card" style="flex:1"><div style="display:flex;justify-content:space-between;margin-bottom:10px;align-items:center"><h3>脚本执行</h3><div style="display:flex;gap:10px"><button id="btnRunInstall" class="btn-green" onclick="startScript('install')" disabled>部署 (install.sh)</button> <button id="btnRunUpdate" class="btn-orange" onclick="startScript('update')" disabled>更新 (mdm.sh)</button></div></div><div id="deploy-term" style="height:400px;background:#000"></div></div></div></div>
    <div id="panel-files" class="panel"><div class="container-box"><div class="card" style="height:100%;padding:0"><div style="padding:15px;background:#f8f9fa;border-bottom:1px solid #eee"><div class="fm-toolbar"><button onclick="fmUpDir()">上级</button><button onclick="fmRefresh()">刷新</button><span id="fmPath" style="margin:0 10px;font-weight:bold">/root</span><input type="file" id="fmUploadInput" style="display:none" onchange="fmDoUpload()"><button onclick="document.getElementById('fmUploadInput').click()">上传</button></div><div id="fmStatus" style="font-size:12px;color:#666;height:15px"></div></div><div class="fm-list" style="overflow:auto;height:100%"><table style="width:100%"><tbody id="fmBody"></tbody></table></div></div></div></div>
    <div id="panel-terminal" class="panel"><div id="sys-term" class="full-term" style="height:100vh"></div></div>
    <div id="panel-logs" class="panel" style="padding:20px;height:100%"><div class="log-layout"><div class="log-sidebar"><div class="log-sidebar-header">日志列表</div><ul class="log-list"><li class="log-item" onclick="viewLog('tomcat', this)"><span>Tomcat</span> <button class="btn-dl-log" onclick="dlLog('tomcat', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('nginx_access', this)"><span>Nginx Access</span> <button class="btn-dl-log" onclick="dlLog('nginx_access', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('nginx_error', this)"><span>Nginx Error</span> <button class="btn-dl-log" onclick="dlLog('nginx_error', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('app_server', this)"><span>App Server</span> <button class="btn-dl-log" onclick="dlLog('app_server', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('emm_backend', this)"><span>EMM Backend</span> <button class="btn-dl-log" onclick="dlLog('emm_backend', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('license', this)"><span>License</span> <button class="btn-dl-log" onclick="dlLog('license', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('platform', this)"><span>Platform</span> <button class="btn-dl-log" onclick="dlLog('platform', event)"><i class="fas fa-download"></i></button></li></ul></div><div class="log-viewer-container"><div class="log-viewer-header"><span id="logTitle">请选择...</span><div><label><input type="checkbox" id="autoScroll" checked> 自动滚动</label> <button class="btn-sm" onclick="clearLog()">清空</button></div></div><div id="logContent" class="log-content"></div></div></div></div>
    <div id="panel-about" class="panel">
        <div class="container-box" style="max-width: 800px;">
            <div class="card">
                <h3>关于智能部署工具</h3>
                <table class="about-table">
                    <tbody>
                        <tr>
                            <td style="width: 100px;"><strong>作者</strong></td>
                            <td>王凯</td>
                        </tr>
                        <tr>
                            <td><strong>版本</strong></td>
                            <td>3.2</td>
                        </tr>
                        <tr>
                            <td><strong>更新日期</strong></td>
                            <td>2024-07-26</td>
                        </tr>
                        <tr>
                            <td style="vertical-align: top; padding-top: 12px;"><strong>主要功能</strong></td>
                            <td>
                                <ul style="margin:0; padding-left: 20px; line-height: 1.8;">
                                    <li>系统基础环境、安全配置、服务状态一键体检</li>
                                    <li>通过上传或本地路径挂载 ISO 镜像，自动配置 YUM 源</li>
                                    <li>在线安装 RPM 依赖包</li>
                                    <li>上传部署包并执行安装/更新脚本</li>
                                    <li>图形化文件管理（浏览、上传、下载）</li>
                                    <li>全功能网页 Shell 终端</li>
                                    <li>实时查看多种 UEM 服务日志</li>
                                </ul>
                            </td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>
</div>
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
<script>
    const API_BASE = "api/"; const UPLOAD_URL = "upload";
    if (!location.pathname.endsWith('/')) window.location.href = location.pathname + '/' + location.search;
    let deployTerm, sysTerm, deploySocket, sysSocket, deployFit, sysFit, logSocket, currentPath = "/root";
    window.onload = function() { runCheck(); fmLoadPath("/root"); }
    function switchTab(id) {
        document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
        document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        document.getElementById('panel-'+id).classList.add('active'); event.target.classList.add('active');
        if (id === 'terminal') { if (!sysTerm) initSysTerm(); setTimeout(()=>sysFit.fit(), 200); }
        if (id === 'deploy') { setTimeout(()=>deployFit && deployFit.fit(), 200); }
    }
    function getWsUrl(ep) { return (location.protocol==='https:'?'wss://':'ws://') + location.host + location.pathname + ep; }
    function viewLog(key, el) {
        document.querySelectorAll('.log-item').forEach(l=>l.classList.remove('active')); el.classList.add('active');
        document.getElementById('logTitle').innerText = "Log: " + key;
        const box = document.getElementById('logContent'); box.innerText = "Connecting...\n";
        if(logSocket) logSocket.close();
        logSocket = new WebSocket(getWsUrl("ws/log?key="+key));
        logSocket.onmessage = e => { box.innerText += e.data; if(box.innerText.length>50000) box.innerText=box.innerText.substring(box.innerText.length-50000); if(document.getElementById('autoScroll').checked) box.scrollTop=box.scrollHeight; };
        logSocket.onclose = () => { box.innerText += "\n>>> Disconnected"; };
    }
    function dlLog(key, e) { e.stopPropagation(); window.location.href = API_BASE + 'log/download?key=' + key; }
    function clearLog(){ document.getElementById('logContent').innerText=""; }
    async function runCheck() {
        try {
            const resp = await fetch(API_BASE + 'check'); const data = await resp.json();
            let baseHtml = '';
            baseHtml += row('CPU', data.sys_info.cpu_cores + ' 核', data.sys_info.cpu_pass);
            baseHtml += row('内存', data.sys_info.mem_total, data.sys_info.mem_pass);
            baseHtml += row('架构', data.sys_info.arch, true);
            baseHtml += row('操作系统', data.sys_info.os_name, data.sys_info.os_pass);
            baseHtml += '<tr><td>性能(ulimit)</td><td>'+data.sys_info.ulimit+'</td><td>'+(data.sys_info.ulimit_pass?'<span class="pass">OK</span>':'<span class="warn">Opt</span>')+'</td></tr>';
            document.getElementById('baseTable').innerHTML = baseHtml;
            let secHtml = '';
            secHtml += '<tr><td>SELinux</td><td>'+data.sec_info.selinux+'</td><td>'+(data.sec_info.selinux==="Disabled"||data.sec_info.selinux==="Permissive"?'<span class="pass">OK</span>':'<button class="btn-sm btn-fix" onclick="fixSelinux()">⛔ 关闭</button>')+'</td></tr>';
            secHtml += '<tr><td>防火墙</td><td>'+data.sec_info.firewall+'</td><td>'+(data.sec_info.firewall==="Stopped"?'<span class="pass">OK</span>':'<button class="btn-sm btn-fix" onclick="fixFirewall()">⛔ 关闭</button>')+'</td></tr>';
            let sshBtn = data.sec_info.ssh_tunnel_ok ? '<span class="pass">开启</span>' : '<span class="fail">关闭</span> <button class="btn-sm btn-fix" onclick="fixSsh()">🔧 修复</button>';
            secHtml += '<tr><td>SSH隧道</td><td>TCP转发</td><td>'+sshBtn+'</td></tr>';
            document.getElementById('secTable').innerHTML = secHtml;
            let diskHtml = '<div style="display:flex; flex-direction:column; gap:12px;">';
            data.sys_info.disk_list.forEach(d => {
                let color = d.usage>=90?'bg-red':(d.usage>=75?'bg-orange':'bg-green');
                diskHtml += '<div><div style="font-weight:bold;margin-bottom:4px;font-size:13px;">'+d.mount+' <span style="color:#666">('+d.usage+'%)</span></div><div class="progress-bg"><div class="progress-bar '+color+'" style="width:'+d.usage+'%"></div></div><div class="disk-text"><span>'+d.used+'</span><span>'+d.total+'</span></div></div>';
            });
            document.getElementById('diskList').innerHTML = diskHtml + '</div>';
            const uemBox = document.getElementById('uemStatusBox');
            if (!data.uem_info.installed) { uemBox.innerHTML = '<div style="color:#7f8c8d;text-align:center;padding:20px;">未检测到 UEM</div>'; } 
            else {
                let h = '<table style="width:100%"><thead><tr><th>服务</th><th>状态</th><th>操作</th></tr></thead><tbody>';
                data.uem_info.services.forEach(s => {
                    let st = s.status==='running'?'<span class="pass">Run</span>':'<span class="fail">Stop</span>';
                    h += '<tr><td>'+s.name+'</td><td>'+st+'</td><td><button class="btn-sm btn-restart" onclick="restartService(\''+s.name+'\')">重启</button></td></tr>';
                });
                uemBox.innerHTML = h + '</tbody></table>';
            }
            let mHtml = !data.minio_info.bucket_exists ? '<tr><td>Err</td><td colspan="2">桶不存在/未连接</td></tr>' : '<tr><td>nqsky</td><td>'+data.minio_info.policy+'</td><td>'+(data.minio_info.policy==='public'?'<span class="pass">OK</span>':'<button class="btn-sm btn-fix" onclick="fixMinio()">Public</button>')+'</td></tr>';
            document.getElementById('minioTable').innerHTML = mHtml;
        } catch(e) {}
    }
    function row(name, val, pass) { return '<tr><td>'+name+'</td><td>'+val+'</td><td>'+(pass?'<span class="pass">OK</span>':'<span class="fail">Fail</span>')+'</td></tr>'; }
    async function fixSelinux() { if(confirm("关闭 SELinux (需重启)？")) fetch(API_BASE+'sec/selinux',{method:'POST'}).then(r=>r.text()).then(t=>{ alert(t); runCheck(); }); }
    async function fixFirewall() { if(confirm("关闭防火墙？")) fetch(API_BASE+'sec/firewall',{method:'POST'}).then(r=>r.text()).then(alert).then(runCheck); }
    async function restartService(n) { if(confirm('重启 '+n+' ?')) fetch(API_BASE+'service/restart?name='+n,{method:'POST'}).then(r=>r.text()).then(alert).then(runCheck); }
    async function fixMinio() { if(confirm("Public?")) fetch(API_BASE+'minio/fix',{method:'POST'}).then(r=>r.text()).then(alert).then(runCheck); }
    async function fixSsh() { if(confirm("Fix SSH?")) fetch(API_BASE+'fix_ssh',{method:'POST'}).then(r=>r.text()).then(alert); }
    async function fmLoadPath(p) {
        currentPath=p; document.getElementById('fmPath').innerText=p;
        const r=await fetch(API_BASE+'fs/list?path='+encodeURIComponent(p)); const fs=await r.json();
        let h=''; fs.sort((a,b)=>(a.is_dir===b.is_dir)?0:a.is_dir?-1:1);
        fs.forEach(f=>{
            let n=f.is_dir?'<a class="link-dir" href="javascript:fmLoadPath(\''+f.path+'\')">'+f.name+'</a>':f.name;
            let act=f.is_dir?'':'<button class="btn-sm" onclick="fmDownload(\''+f.path+'\')">下载</button>';
            h+='<tr><td>'+(f.is_dir?'📁':'📄')+' '+n+'</td><td>'+f.size+'</td><td>'+f.mod_time+'</td><td>'+act+'</td></tr>';
        });
        document.getElementById('fmBody').innerHTML=h;
    }
    function fmUpDir() { let p=currentPath.split('/'); p.pop(); let n=p.join('/'); if(!n)n='/'; fmLoadPath(n); }
    function fmDownload(p) { window.location.href = API_BASE + 'fs/download?path=' + encodeURIComponent(p); }
    async function fmDoUpload() { const inp=document.getElementById('fmUploadInput'); const fd=new FormData(); fd.append("file", inp.files[0]); fd.append("path", currentPath); const st=document.getElementById('fmStatus'); st.innerText="Uploading..."; await fetch(API_BASE+'upload_any', {method:'POST', body:fd}); st.innerText="Done"; fmLoadPath(currentPath); }
    
    // --- ISO ---
    async function mountIso() {
        const inp=document.getElementById('isoInput'); if(!inp.files.length)return; event.target.disabled=true; const fd=new FormData(); fd.append("file",inp.files[0]);
        const r=await fetch(API_BASE+'iso_mount',{method:'POST',body:fd}); const rd=r.body.getReader(); const d=new TextDecoder(); const box=document.getElementById('yum-log'); while(true){const{done,value}=await rd.read();if(done)break;box.innerText+=d.decode(value);box.scrollTop=box.scrollHeight;} event.target.disabled=false;
    }
    async function mountLocalIso() {
        const p = document.getElementById('isoPathInput').value; if(!p) return alert("请输入路径"); event.target.disabled=true;
        const fd=new FormData(); fd.append("path", p);
        const r=await fetch(API_BASE+'iso_mount_local',{method:'POST',body:fd}); const rd=r.body.getReader(); const d=new TextDecoder(); const box=document.getElementById('yum-log'); box.innerText = ">>> 正在使用本地文件挂载...\n";
        while(true){const{done,value}=await rd.read();if(done)break;box.innerText+=d.decode(value);box.scrollTop=box.scrollHeight;} event.target.disabled=false;
    }

    async function installRpm() { const i=document.getElementById('rpmInput'); if(!i.files.length)return; event.target.disabled=true; const fd=new FormData(); fd.append("file",i.files[0]); const r=await fetch(API_BASE+'rpm_install',{method:'POST',body:fd}); const rd=r.body.getReader(); const d=new TextDecoder(); const box=document.getElementById('rpm-log'); while(true){const{done,value}=await rd.read();if(done)break;box.innerText+=d.decode(value);box.scrollTop=box.scrollHeight;} event.target.disabled=false; }
    async function uploadFile() { const i=document.getElementById('fileInput'); if(!i.files.length)return; event.target.disabled=true; const fd=new FormData(); fd.append("file", i.files[0]); try { const r=await fetch(UPLOAD_URL, {method:'POST', body:fd}); if(r.ok) { document.getElementById('uploadStatus').innerHTML = "<span class='pass'>✅ 成功</span>"; document.getElementById('btnRunInstall').disabled=false; document.getElementById('btnRunUpdate').disabled=false; } else { throw await r.text(); } } catch(e){alert("Error: "+e);} event.target.disabled=false; }
    function startScript(type) { if(deployTerm) deployTerm.dispose(); if(deploySocket) deploySocket.close(); deployTerm=new Terminal({cursorBlink:true,fontSize:13,theme:{background:'#000'}}); deployFit=new FitAddon.FitAddon(); deployTerm.loadAddon(deployFit); deployTerm.open(document.getElementById('deploy-term')); deployFit.fit(); deploySocket=new WebSocket(getWsUrl("ws/deploy?type="+type)); setupSocket(deploySocket, deployTerm, deployFit); document.getElementById('btnRunInstall').disabled=true; document.getElementById('btnRunUpdate').disabled=true; }
    function startDeployTerm() { startScript('install'); }
    function initSysTerm() { sysTerm=new Terminal({cursorBlink:true,fontSize:14,fontFamily:'Consolas, monospace'}); sysFit=new FitAddon.FitAddon(); sysTerm.loadAddon(sysFit); sysTerm.open(document.getElementById('sys-term')); sysFit.fit(); sysSocket=new WebSocket(getWsUrl("ws/terminal")); setupSocket(sysSocket, sysTerm, sysFit); }
    function setupSocket(s, t, f) { s.onopen=()=>{s.send(JSON.stringify({type:"resize",cols:t.cols,rows:t.rows}));f.fit();}; s.onmessage=e=>t.write(e.data); t.onData(d=>{if(s.readyState===1)s.send(JSON.stringify({type:"input",data:d}));}); window.addEventListener('resize',()=>{f.fit();if(s.readyState===1)s.send(JSON.stringify({type:"resize",cols:t.cols,rows:t.rows}));}); }
</script>
</body>
</html>
`

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// 数据结构
type DiskInfo struct {
	Mount string `json:"mount"`
	Total string `json:"total"`
	Used  string `json:"used"`
	Usage int    `json:"usage"`
}
type SysInfo struct {
	CpuCores   int        `json:"cpu_cores"`
	CpuPass    bool       `json:"cpu_pass"`
	MemTotal   string     `json:"mem_total"`
	MemPass    bool       `json:"mem_pass"`
	Arch       string     `json:"arch"`
	OsName     string     `json:"os_name"`
	OsPass     bool       `json:"os_pass"`
	DiskList   []DiskInfo `json:"disk_list"`
	DiskDetail string     `json:"disk_detail"`
	Ulimit     string     `json:"ulimit"`
	UlimitPass bool       `json:"ulimit_pass"`
}
type SecInfo struct {
	SELinux     string `json:"selinux"`
	Firewall    string `json:"firewall"`
	SshTunnelOk bool   `json:"ssh_tunnel_ok"`
}
type ServiceStat struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type UemInfo struct {
	Installed bool          `json:"installed"`
	Services  []ServiceStat `json:"services"`
}
type MinioInfo struct {
	BucketExists bool   `json:"bucket_exists"`
	Policy       string `json:"policy"`
}
type FullCheckResult struct {
	SysInfo   SysInfo   `json:"sys_info"`
	SecInfo   SecInfo   `json:"sec_info"`
	UemInfo   UemInfo   `json:"uem_info"`
	MinioInfo MinioInfo `json:"minio_info"`
}
type FileInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    string `json:"size"`
	ModTime string `json:"mod_time"`
}
type WSMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func main() {
	flag.StringVar(&ServerPort, "port", "9898", "Server listening port")
	flag.Parse()
	os.MkdirAll(RpmCacheDir, 0755)
	autoFixSshConfig()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(htmlPage))
	})
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/api/upload_any", handleUploadAny)
	http.HandleFunc("/api/fs/list", handleFsList)
	http.HandleFunc("/api/fs/download", handleFsDownload)
	http.HandleFunc("/api/check", handleCheckEnv)
	http.HandleFunc("/api/service/restart", handleRestartService)
	http.HandleFunc("/api/minio/fix", handleFixMinio)
	http.HandleFunc("/api/fix_ssh", handleFixSsh)
	http.HandleFunc("/api/sec/selinux", handleFixSelinux)
	http.HandleFunc("/api/sec/firewall", handleFixFirewall)
	http.HandleFunc("/api/rpm_install", handleRpmInstall)
	http.HandleFunc("/api/iso_mount", handleIsoMount)
	http.HandleFunc("/api/iso_mount_local", handleIsoMountLocal) // 新增
	http.HandleFunc("/api/log/download", handleLogDownload)

	http.HandleFunc("/ws/deploy", handleDeployWS)
	http.HandleFunc("/ws/terminal", handleSysTermWS)
	http.HandleFunc("/ws/log", handleLogWS)

	fmt.Printf("Agent running on %s\n", ServerPort)
	http.ListenAndServe("0.0.0.0:"+ServerPort, nil)
}

// ---------------- 业务逻辑 ----------------

// ISO Mount (Upload mode)
func handleIsoMount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	f, _ := w.(http.Flusher)
	fmt.Fprintf(w, ">>> Upload ISO...\n")
	f.Flush()
	r.ParseMultipartForm(10 << 30)
	file, _, _ := r.FormFile("file")
	defer file.Close()
	dst, _ := os.Create(IsoSavePath)
	io.Copy(dst, file)
	dst.Close()
	mountAndConfigRepo(w, IsoSavePath)
}

// ISO Mount (Local mode) - NEW
func handleIsoMountLocal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	f, _ := w.(http.Flusher)
	path := r.FormValue("path")
	fmt.Fprintf(w, ">>> Checking local file: %s ...\n", path)
	f.Flush()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintf(w, "❌ File not found: %s\n", path)
		return
	}
	// Copy or Link? Link is faster but mount -o loop needs file.
	// Let's just use the path directly to mount.
	// But wait, if we want to standardize on IsoSavePath for consistency?
	// Let's just symlink it to IsoSavePath so existing logic works, OR just pass path.
	// Passing path is cleaner.
	mountAndConfigRepo(w, path)
}

// Shared logic for mounting and repo setup
func mountAndConfigRepo(w http.ResponseWriter, isoPath string) {
	f, ok := w.(http.Flusher)
	fmt.Fprintf(w, ">>> Mounting %s to %s ...\n", isoPath, IsoMountPoint)
	if ok {
		f.Flush()
	}

	os.MkdirAll(IsoMountPoint, 0755)
	exec.Command("umount", IsoMountPoint).Run()

	if out, err := exec.Command("mount", "-o", "loop", isoPath, IsoMountPoint).CombinedOutput(); err != nil {
		fmt.Fprintf(w, "❌ Mount failed: %s\n", string(out))
		return
	}

	fmt.Fprintf(w, "✅ Mount success. Configuring Repo...\n")
	os.MkdirAll(RepoBackupDir, 0755)
	exec.Command("bash", "-c", fmt.Sprintf("mv /etc/yum.repos.d/*.repo %s/", RepoBackupDir)).Run()

	rc := ""
	// RHEL 8/9 style
	if _, err := os.Stat(filepath.Join(IsoMountPoint, "BaseOS")); err == nil {
		rc += fmt.Sprintf("[Local-BaseOS]\nname=BaseOS\nbaseurl=file://%s/BaseOS\ngpgcheck=0\nenabled=1\n\n", IsoMountPoint)
	}
	if _, err := os.Stat(filepath.Join(IsoMountPoint, "AppStream")); err == nil {
		rc += fmt.Sprintf("[Local-AppStream]\nname=AppStream\nbaseurl=file://%s/AppStream\ngpgcheck=0\nenabled=1\n", IsoMountPoint)
	}
	// CentOS 7 style
	if rc == "" {
		rc = fmt.Sprintf("[Local-ISO]\nname=Local ISO\nbaseurl=file://%s\ngpgcheck=0\nenabled=1\n", IsoMountPoint)
	}

	os.WriteFile("/etc/yum.repos.d/local.repo", []byte(rc), 0644)
	fmt.Fprintf(w, "✅ Repo written. Cleaning cache...\n")
	if ok {
		f.Flush()
	}

	c := exec.Command("bash", "-c", "yum clean all && yum makecache")
	s, _ := c.StdoutPipe()
	c.Stderr = c.Stdout
	c.Start()
	sc := bufio.NewScanner(s)
	for sc.Scan() {
		fmt.Fprintln(w, sc.Text())
		if ok {
			f.Flush()
		}
	}
	c.Wait()
	fmt.Fprintf(w, "\n🎉 All Done.\n")
	if ok {
		f.Flush()
	}
}

func handleLogDownload(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	path, ok := logFileMap[key]
	if !ok {
		http.Error(w, "Unknown Log", 400)
		return
	}
	os.Chmod(path, 0644)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func handleLogWS(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	path, ok := logFileMap[key]
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	if !ok {
		conn.WriteMessage(websocket.TextMessage, []byte("Unknown Log Key"))
		return
	}
	os.Chmod(path, 0644)
	if info, err := os.Stat(path); os.IsNotExist(err) {
		conn.WriteMessage(websocket.TextMessage, []byte("❌ 错误: 日志文件不存在: "+path))
		return
	} else if info.Size() == 0 {
		conn.WriteMessage(websocket.TextMessage, []byte(">>> 提示：日志文件为空，等待写入...\n"))
	}
	cmd := exec.Command("tail", "-f", "-n", "200", path)
	cmd.Stderr = cmd.Stdout
	out, _ := cmd.StdoutPipe()
	cmd.Start()
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	buf := make([]byte, 4096)
	for {
		n, err := out.Read(buf)
		if err != nil {
			break
		}
		if conn.WriteMessage(websocket.TextMessage, buf[:n]) != nil {
			break
		}
	}
}

func handleCheckEnv(w http.ResponseWriter, r *http.Request) {
	res := FullCheckResult{}
	res.SysInfo.CpuCores = runtime.NumCPU()
	res.SysInfo.CpuPass = res.SysInfo.CpuCores >= 2
	mkb := getMemTotalKB()
	res.SysInfo.MemTotal = fmt.Sprintf("%.1f GB", float64(mkb)/1024/1024)
	res.SysInfo.MemPass = float64(mkb)/1024/1024 >= 7.5
	res.SysInfo.Arch = runtime.GOARCH
	res.SysInfo.OsName = getOSName()
	lo := strings.ToLower(res.SysInfo.OsName)
	res.SysInfo.OsPass = (strings.Contains(lo, "kylin") && strings.Contains(lo, "v10")) || (strings.Contains(lo, "rocky") && strings.Contains(lo, "9"))
	out, _ := exec.Command("bash", "-c", "ulimit -n").Output()
	res.SysInfo.Ulimit = strings.TrimSpace(string(out))
	res.SysInfo.UlimitPass = (res.SysInfo.Ulimit != "1024")
	cmd := exec.Command("df", "-h")
	out, _ = cmd.Output()
	res.SysInfo.DiskDetail = string(out)
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i == 0 || len(line) == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 6 && !strings.Contains(f[0], "tmpfs") && !strings.Contains(f[0], "overlay") {
			u, _ := strconv.Atoi(strings.TrimRight(f[4], "%"))
			res.SysInfo.DiskList = append(res.SysInfo.DiskList, DiskInfo{Mount: f[5], Total: f[1], Used: f[2], Usage: u})
		}
	}
	if out, err := exec.Command("getenforce").Output(); err == nil {
		res.SecInfo.SELinux = strings.TrimSpace(string(out))
	} else {
		res.SecInfo.SELinux = "Unknown"
	}
	if err := exec.Command("systemctl", "is-active", "firewalld").Run(); err == nil {
		res.SecInfo.Firewall = "Running"
	} else {
		res.SecInfo.Firewall = "Stopped"
	}
	res.SecInfo.SshTunnelOk = checkSshConfig()
	if _, err := os.Stat("/opt/emm/current"); err == nil {
		res.UemInfo.Installed = true
		for _, svc := range uemServices {
			st := "stopped"
			if err := exec.Command("systemctl", "is-active", svc).Run(); err == nil {
				st = "running"
			} else if err := exec.Command("pgrep", "-f", svc).Run(); err == nil {
				st = "running"
			}
			res.UemInfo.Services = append(res.UemInfo.Services, ServiceStat{Name: svc, Status: st})
		}
	}
	ctx := context.Background()
	mClient, err := minio.New(MinioEndpoint, &minio.Options{Creds: credentials.NewStaticV4(MinioUser, MinioPass, ""), Secure: false})
	if err == nil {
		exists, errBucket := mClient.BucketExists(ctx, MinioBucket)
		if errBucket == nil && exists {
			res.MinioInfo.BucketExists = true
			policy, errPol := mClient.GetBucketPolicy(ctx, MinioBucket)
			if errPol == nil && (strings.Contains(policy, "s3:GetObject") && strings.Contains(policy, "AWS\":[\"*\"]")) {
				res.MinioInfo.Policy = "public"
			} else {
				res.MinioInfo.Policy = "private"
			}
		}
	}
	json.NewEncoder(w).Encode(res)
}

func handleFixMinio(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST", 400)
		return
	}
	ctx := context.Background()
	mClient, err := minio.New(MinioEndpoint, &minio.Options{Creds: credentials.NewStaticV4(MinioUser, MinioPass, ""), Secure: false})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetBucketLocation","s3:ListBucket"],"Resource":["arn:aws:s3:::%s"]},{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, MinioBucket, MinioBucket)
	if err := mClient.SetBucketPolicy(ctx, MinioBucket, policy); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Write([]byte("✅ 权限已修改为 Public"))
}

func handleRestartService(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if out, err := exec.Command("systemctl", "restart", name).CombinedOutput(); err != nil {
		http.Error(w, string(out), 500)
		return
	}
	w.Write([]byte("✅ 指令已发送"))
}
func handleFixSelinux(w http.ResponseWriter, r *http.Request) {
	exec.Command("setenforce", "0").Run()
	cfg := "/etc/selinux/config"
	d, _ := os.ReadFile(cfg)
	l := strings.Split(string(d), "\n")
	var n []string
	for _, s := range l {
		if strings.HasPrefix(strings.TrimSpace(s), "SELINUX=") {
			n = append(n, "SELINUX=disabled")
		} else {
			n = append(n, s)
		}
	}
	os.WriteFile(cfg, []byte(strings.Join(n, "\n")), 0644)
	w.Write([]byte("✅ SELinux Disabled"))
}
func handleFixFirewall(w http.ResponseWriter, r *http.Request) {
	exec.Command("systemctl", "stop", "firewalld").Run()
	exec.Command("systemctl", "disable", "firewalld").Run()
	exec.Command("ufw", "disable").Run()
	exec.Command("iptables", "-F").Run()
	w.Write([]byte("✅ 防火墙已关闭"))
}
func handleFixSsh(w http.ResponseWriter, r *http.Request) {
	autoFixSshConfig()
	w.Write([]byte("✅ 修复指令已发送"))
}
func autoFixSshConfig() error {
	cfg := "/etc/ssh/sshd_config"
	d, _ := os.ReadFile(cfg)
	lines := strings.Split(string(d), "\n")
	hasY, hasN := false, false
	for _, l := range lines {
		t := strings.ToLower(strings.TrimSpace(l))
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "allowtcpforwarding") {
			if strings.Contains(t, "yes") {
				hasY = true
			}
			if strings.Contains(t, "no") {
				hasN = true
			}
		}
	}
	if hasY && !hasN {
		return nil
	}
	os.WriteFile(cfg+".bak_cncy", d, 0644)
	var newL []string
	for _, l := range lines {
		if !strings.Contains(strings.ToLower(l), "allowtcpforwarding") {
			newL = append(newL, l)
		}
	}
	newL = append(newL, "", "AllowTcpForwarding yes", "")
	os.WriteFile(cfg, []byte(strings.Join(newL, "\n")), 0644)
	exec.Command("systemctl", "restart", "sshd").Run()
	exec.Command("service", "sshd", "restart").Run()
	time.Sleep(1 * time.Second)
	return nil
}
func checkSshConfig() bool {
	f, _ := os.Open("/etc/ssh/sshd_config")
	defer f.Close()
	s := bufio.NewScanner(f)
	y, n := false, false
	for s.Scan() {
		l := strings.ToLower(strings.TrimSpace(s.Text()))
		if strings.HasPrefix(l, "#") {
			continue
		}
		if strings.HasPrefix(l, "allowtcpforwarding") {
			if strings.Contains(l, "yes") {
				y = true
			}
			if strings.Contains(l, "no") {
				n = true
			}
		}
	}
	return y && !n
}
func handleUpload(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(500 << 20)
	f, h, _ := r.FormFile("file")
	defer f.Close()
	dst, _ := os.Create(filepath.Join(UploadTargetDir, h.Filename))
	defer dst.Close()
	io.Copy(dst, f)
	exec.Command("tar", "-zxvf", filepath.Join(UploadTargetDir, h.Filename), "-C", UploadTargetDir).Run()
	w.Write([]byte("OK"))
}
func handleUploadAny(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(500 << 20)
	f, h, _ := r.FormFile("file")
	defer f.Close()
	d := r.FormValue("path")
	if d == "" {
		d = UploadTargetDir
	}
	dst, _ := os.Create(filepath.Join(d, h.Filename))
	defer dst.Close()
	io.Copy(dst, f)
	w.WriteHeader(200)
}
func handleFsList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "/root"
	}
	es, _ := os.ReadDir(dir)
	var fs []FileInfo
	for _, e := range es {
		i, _ := e.Info()
		sz := "-"
		if !e.IsDir() {
			sz = formatBytes(i.Size())
		}
		fs = append(fs, FileInfo{Name: e.Name(), Path: filepath.Join(dir, e.Name()), IsDir: e.IsDir(), Size: sz, ModTime: i.ModTime().Format("2006-01-02 15:04")})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fs)
}
func handleFsDownload(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(p)))
	http.ServeFile(w, r, p)
}
func handleRpmInstall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Transfer-Encoding", "chunked")
	f, _ := w.(http.Flusher)
	fmt.Fprintf(w, ">>> Upload...\n")
	f.Flush()
	r.ParseMultipartForm(500 << 20)
	file, h, _ := r.FormFile("file")
	defer file.Close()
	p := filepath.Join(RpmCacheDir, h.Filename)
	d, _ := os.Create(p)
	io.Copy(d, file)
	d.Close()
	fmt.Fprintf(w, ">>> rpm -Uvh...\n")
	f.Flush()
	c := exec.Command("rpm", "-Uvh", "--replacepkgs", p)
	s, _ := c.StdoutPipe()
	c.Stderr = c.Stdout
	c.Start()
	sc := bufio.NewScanner(s)
	for sc.Scan() {
		fmt.Fprintln(w, sc.Text())
		f.Flush()
	}
	c.Wait()
	fmt.Fprintf(w, "\nDone.\n")
	f.Flush()
}

func handleDeployWS(w http.ResponseWriter, r *http.Request) {
	scriptType := r.URL.Query().Get("type")
	var scriptPath string
	var scriptName string

	if scriptType == "install" {
		scriptName = InstallScript
	} else if scriptType == "update" {
		scriptName = UpdateScript
	} else {
		http.Error(w, "Invalid script type", 400)
		return
	}

	scriptPath = filepath.Join(InstallWorkDir, scriptName)

	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		msg := fmt.Sprintf("❌ Error: Script '%s' not found in '%s'", scriptName, InstallWorkDir)
		conn.WriteMessage(websocket.TextMessage, []byte(msg))
		return
	}
	os.Chmod(scriptPath, 0755)

	c := exec.Command("/bin/bash", scriptPath)
	c.Dir = InstallWorkDir
	c.Env = append(os.Environ(), "TERM=xterm-256color", "HOME=/root")
	startPTYSession(w, r, c)
}

func handleSysTermWS(w http.ResponseWriter, r *http.Request) {
	sh := "/bin/bash"
	if _, err := os.Stat(sh); os.IsNotExist(err) {
		sh = "/bin/sh"
	}
	c := exec.Command(sh)
	c.Env = append(os.Environ(), "TERM=xterm-256color", "HOME=/root")
	c.Dir = "/root"
	startPTYSession(w, r, c)
}
func startPTYSession(w http.ResponseWriter, r *http.Request, c *exec.Cmd) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ptmx, tty, err := pty.Open()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Err:"+err.Error()))
		return
	}
	defer tty.Close()
	c.Stdout = tty
	c.Stdin = tty
	c.Stderr = tty
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setsid = true
	c.SysProcAttr.Setctty = false
	if err := c.Start(); err != nil {
		ptmx.Close()
		conn.WriteMessage(websocket.TextMessage, []byte("Start Err:"+err.Error()))
		return
	}
	defer func() { _ = ptmx.Close(); _ = c.Process.Kill(); _ = c.Wait() }()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			if conn.WriteMessage(websocket.TextMessage, buf[:n]) != nil {
				return
			}
		}
	}()
	for {
		_, m, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg WSMessage
		if json.Unmarshal(m, &msg) == nil {
			if msg.Type == "input" {
				ptmx.Write([]byte(msg.Data))
			} else if msg.Type == "resize" {
				pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(msg.Rows), Cols: uint16(msg.Cols)})
			}
		}
	}
}
func formatBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%dB", b)
	}
	d, e := int64(u), 0
	for n := b / u; n >= u; n /= u {
		d *= u
		e++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(d), "KMGTPE"[e])
}
func getMemTotalKB() uint64 {
	f, _ := os.Open("/proc/meminfo")
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		l := s.Text()
		if strings.HasPrefix(l, "MemTotal:") {
			var k uint64
			fmt.Sscanf(strings.Fields(l)[1], "%d", &k)
			return k
		}
	}
	return 0
}
func getDiskTotalGB(path string) float64 {
	var s syscall.Statfs_t
	if syscall.Statfs(path, &s) != nil {
		return 0
	}
	return float64(s.Blocks) * float64(s.Bsize) / 1024 / 1024 / 1024
}
func getOSName() string {
	f, _ := os.Open("/etc/os-release")
	defer f.Close()
	s := bufio.NewScanner(f)
	n, v := "", ""
	for s.Scan() {
		l := s.Text()
		if strings.HasPrefix(l, "NAME=") {
			n = strings.Trim(strings.Split(l, "=")[1], "\"")
		}
		if strings.HasPrefix(l, "VERSION=") {
			v = strings.Trim(strings.Split(l, "=")[1], "\"")
		}
	}
	return n + " " + v
}
