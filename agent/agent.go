package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/go-redis/redis/v8"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/websocket"
	"github.com/magiconair/properties"
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

	// MinIO API
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
	"tomcat":      "/opt/emm/current/tomcat/logs/catalina.out",
	"app_server":  "/emm/logs/AppServer/appServer.log",
	"emm_backend": "/emm/logs/emm_backend/emmBackend.log",
	"license":     "/emm/logs/licenseServer/licenseServer.log",
	"platform":    "/emm/logs/platform/platform.log",
}

// --- BaseServices Structs ---
type Config struct {
	RedisHost           string `properties:"system.redis.host"`
	RedisPort           int    `properties:"system.redis.port"`
	RedisPassword       string `properties:"system.redis.password"`
	MdmJdbcURL          string `properties:"jdbc.url"`
	MdmJdbcUsername     string `properties:"jdbc.username"`
	MdmJdbcPassword     string `properties:"jdbc.password"`
	MtenantJdbcURL      string `properties:"jdbc.multitenant.url"`
	MtenantJdbcUsername string `properties:"jdbc.multitenant.username"`
	MtenantJdbcPassword string `properties:"jdbc.multitenant.password"`
	RabbitMQAddresses   string `properties:"spring.rabbitmq.addresses"`
	RabbitMQAdminPort   int    `properties:"rabbitmq.admin.port,default=15672"`
	MinioURL            string `properties:"storage.minio.url"`
}

type Metric struct {
	Time            int64  `json:"time"`
	Uptime          int64  `json:"uptime"`
	UptimeStr       string `json:"uptime_str"`
	Threads         int    `json:"threads"`
	QPS             int    `json:"qps"`
	MaxConnections  int    `json:"max_connections"`
	SlowQueries     int    `json:"slow_queries"`
	OpenTables      int    `json:"open_tables"`
	InnoDBBuffUsed  int    `json:"innodb_buff_used"`
	InnoDBBuffTotal int    `json:"innodb_buff_total"`
}

type TableStat struct {
	Name   string `json:"name"`
	Rows   int    `json:"rows"`
	SizeMB int    `json:"size_mb"`
	Ops    int    `json:"ops"`
}

type ProcessListRow struct {
	Id      int    `json:"id"`
	User    string `json:"user"`
	Host    string `json:"host"`
	DB      string `json:"db"`
	Command string `json:"command"`
	Time    int    `json:"time"`
	State   string `json:"state"`
	Info    string `json:"info"`
}

type ReplicationStatus struct {
	Role          string `json:"role"`
	SlaveRunning  bool   `json:"slave_running"`
	SecondsBehind int    `json:"seconds_behind"`
}

type SqlResult struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Error   string     `json:"error,omitempty"`
}

var appConfig Config
var (
	rdb           *redis.Client
	dbConnections map[string]*sql.DB
	ctx           = context.Background()
	lastQuestions int64
	lastQTime     time.Time
	qpsMutex      sync.Mutex
	lastNetRx     uint64
	lastNetTx     uint64
	lastNetTime   time.Time
	netMutex      sync.Mutex
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// Data Structures
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
	MemUsage   float64    `json:"mem_usage"`
	LoadAvg    float64    `json:"load_avg"`
	NetRx      float64    `json:"net_rx"`
	NetTx      float64    `json:"net_tx"`
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

	// 2. 确保其他初始化逻辑保留
	initLogPaths()
	loadConfig()
	initRedis()
	initMySQL()

	// 3. 路由注册
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
	http.HandleFunc("/api/iso_mount_local", handleIsoMountLocal)
	http.HandleFunc("/api/log/download", handleLogDownload)
	http.HandleFunc("/ws/deploy", handleDeployWS)
	http.HandleFunc("/ws/terminal", handleSysTermWS)
	http.HandleFunc("/ws/log", handleLogWS)
	bsAPI := "/api/baseservices"
	http.HandleFunc(bsAPI+"/redis/keys", redisKeysAndTypesHandler)
	http.HandleFunc(bsAPI+"/redis/info", redisInfoHandler)
	http.HandleFunc(bsAPI+"/redis/key", redisKeyHandler)
	http.HandleFunc(bsAPI+"/redis/value", redisValueHandler)
	http.HandleFunc(bsAPI+"/mysql/metrics/", apiMetrics)
	http.HandleFunc(bsAPI+"/mysql/tables/", apiTables)
	http.HandleFunc(bsAPI+"/mysql/processlist/", apiProcesslist)
	http.HandleFunc(bsAPI+"/mysql/replstatus/", apiRepl)
	http.HandleFunc(bsAPI+"/mysql/execsql/", executeSQL)
	setupProxies(bsAPI)

	fmt.Printf("Agent running on %s\n", ServerPort)
	http.ListenAndServe("0.0.0.0:"+ServerPort, nil)
}

// Helper Functions
func initLogPaths() {
	resolveLog := func(primary, fallback string) string {
		if _, err := os.Stat(primary); err == nil {
			return primary
		}
		if _, err := os.Stat(fallback); err == nil {
			return fallback
		}
		return primary
	}
	logFileMap["nginx_access"] = resolveLog("/var/log/nginx/access.log", "/usr/local/nginx/logs/access.log")
	logFileMap["nginx_error"] = resolveLog("/var/log/nginx/error.log", "/usr/local/nginx/logs/error.log")
}

func loadConfig() {
	prodPath := "/opt/emm/current/config/global.properties"
	localPath := "global.properties"
	var p *properties.Properties
	var err error
	if _, err = os.Stat(prodPath); err == nil {
		log.Printf("Loading config: %s", prodPath)
		p, err = properties.LoadFile(prodPath, properties.UTF8)
	} else {
		log.Printf("Loading local config: %s", localPath)
		p, err = properties.LoadFile(localPath, properties.UTF8)
	}
	if err != nil {
		log.Printf("Warning: Config error: %v", err)
		return
	}
	if err := p.Decode(&appConfig); err != nil {
		log.Printf("Warning: Decode error: %v", err)
	}
}

func initRedis() {
	if appConfig.RedisHost == "" {
		log.Println("Redis skipped.")
		return
	}
	addr := fmt.Sprintf("%s:%d", appConfig.RedisHost, appConfig.RedisPort)
	rdb = redis.NewClient(&redis.Options{Addr: addr, Password: appConfig.RedisPassword, DB: 0})
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		log.Printf("Redis connect fail: %v", err)
		rdb = nil
	} else {
		log.Println("Redis connected.")
	}
}

func initMySQL() {
	dbConnections = make(map[string]*sql.DB)
	if appConfig.MdmJdbcURL == "" {
		log.Println("MySQL skipped.")
		return
	}
	configs := map[string]map[string]string{
		"mdm":         {"url": appConfig.MdmJdbcURL, "username": appConfig.MdmJdbcUsername, "password": appConfig.MdmJdbcPassword},
		"multitenant": {"url": appConfig.MtenantJdbcURL, "username": appConfig.MtenantJdbcUsername, "password": appConfig.MtenantJdbcPassword},
	}
	for dbName, config := range configs {
		var dsn string
		if temp := strings.Split(config["url"], "//"); len(temp) > 1 {
			parts := strings.Split(temp[1], "/")
			if len(parts) > 1 {
				hostAndPort, dbNameAndParams := parts[0], parts[1]
				dbNameFromURL := strings.Split(dbNameAndParams, "?")[0]
				dsn = fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true", config["username"], config["password"], hostAndPort, dbNameFromURL)
			}
		}
		if dsn == "" {
			continue
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			log.Printf("MySQL %s open error: %v", dbName, err)
			continue
		}
		db.SetConnMaxLifetime(time.Minute * 3)
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
		if err = db.Ping(); err != nil {
			log.Printf("MySQL %s ping error: %v", dbName, err)
			continue
		}
		dbConnections[dbName] = db
		log.Printf("MySQL %s connected", dbName)
	}
}

func setupProxies(basePath string) {
	redirectHTML := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Loading...</title><style>body{margin:0;display:flex;justify-content:center;align-items:center;height:100vh;background:#f5f7fa;color:#666;font-family:sans-serif;}</style><script>window.location.replace(window.location.pathname + "/");</script></head><body><div style="text-align:center">Loading Interface...</div></body></html>`
	rewriteHTML := func(r *http.Response) error {
		r.Header.Del("X-Frame-Options")
		r.Header.Del("Content-Security-Policy")
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "text/html") {
			bodyBytes, err := ioutil.ReadAll(r.Body)
			if err != nil {
				return err
			}
			r.Body.Close()
			bodyString := string(bodyBytes)
			bodyString = strings.ReplaceAll(bodyString, `src="/`, `src="`)
			bodyString = strings.ReplaceAll(bodyString, `href="/`, `href="`)
			bodyString = strings.ReplaceAll(bodyString, `action="/`, `action="`)
			buf := bytes.NewBufferString(bodyString)
			r.Body = ioutil.NopCloser(buf)
			r.ContentLength = int64(buf.Len())
			r.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
		}
		return nil
	}
	if appConfig.RabbitMQAdminPort > 0 {
		rabbitURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", appConfig.RabbitMQAdminPort))
		rabbitProxy := httputil.NewSingleHostReverseProxy(rabbitURL)
		rabbitProxy.ModifyResponse = rewriteHTML
		rabbitProxy.Director = func(req *http.Request) {
			req.URL.Scheme = rabbitURL.Scheme
			req.URL.Host = rabbitURL.Host
			req.Header.Del("Accept-Encoding")
		}
		http.Handle(basePath+"/rabbitmq/", http.StripPrefix(basePath+"/rabbitmq", rabbitProxy))
		http.HandleFunc(basePath+"/rabbitmq", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(redirectHTML)) })
	}
	targetMinio := "http://127.0.0.1:9001"
	if appConfig.MinioURL != "" && !strings.Contains(appConfig.MinioURL, ":9000") {
		targetMinio = appConfig.MinioURL
	}
	minioURL, err := url.Parse(targetMinio)
	if err == nil {
		minioProxy := httputil.NewSingleHostReverseProxy(minioURL)
		minioProxy.ModifyResponse = rewriteHTML
		minioProxy.Director = func(req *http.Request) {
			req.URL.Scheme = minioURL.Scheme
			req.URL.Host = minioURL.Host
			req.Host = minioURL.Host
			req.Header.Del("Accept-Encoding")
			path := req.URL.Path
			// Smart Path Rewrite logic for MinIO Deep Linking
			if idx := strings.LastIndex(path, "/static/"); idx != -1 {
				req.URL.Path = path[idx:]
			} else if idx := strings.LastIndex(path, "/api/v1/"); idx != -1 {
				req.URL.Path = path[idx:]
			} else if idx := strings.LastIndex(path, "/login"); idx != -1 {
				req.URL.Path = path[idx:]
			} else if idx := strings.LastIndex(path, "/ws/"); idx != -1 {
				req.URL.Path = path[idx:]
			} else if idx := strings.LastIndex(path, "/images/"); idx != -1 {
				req.URL.Path = path[idx:]
			} else if idx := strings.LastIndex(path, "/styles/"); idx != -1 {
				req.URL.Path = path[idx:]
			} else if idx := strings.LastIndex(path, "/loader.css"); idx != -1 {
				req.URL.Path = path[idx:]
			} else {
				req.URL.Path = strings.TrimPrefix(path, basePath+"/minio")
			}
		}
		// WebSocket specific proxy for MinIO
		minioWsProxy := httputil.NewSingleHostReverseProxy(minioURL)
		minioWsProxy.Director = func(req *http.Request) {
			req.URL.Scheme = minioURL.Scheme
			req.URL.Host = minioURL.Host
			req.Host = minioURL.Host
			path := req.URL.Path
			if idx := strings.LastIndex(path, "/ws/"); idx != -1 {
				req.URL.Path = path[idx:]
			} else {
				req.URL.Path = strings.TrimPrefix(path, basePath+"/minio")
			}
		}
		http.HandleFunc(basePath+"/minio/", func(w http.ResponseWriter, r *http.Request) {
			if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
				minioWsProxy.ServeHTTP(w, r)
			} else {
				minioProxy.ServeHTTP(w, r)
			}
		})
		http.HandleFunc(basePath+"/minio", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(redirectHTML)) })
	}
}

func redisKeysAndTypesHandler(w http.ResponseWriter, r *http.Request) {
	if rdb == nil {
		http.Error(w, "Redis not connected", 503)
		return
	}
	var cursor uint64
	var allKeys []string
	for {
		keys, c, err := rdb.Scan(r.Context(), cursor, "*", 500).Result()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		allKeys = append(allKeys, keys...)
		cursor = c
		if cursor == 0 {
			break
		}
	}
	if len(allKeys) == 0 {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "[]")
		return
	}
	pipe := rdb.Pipeline()
	keyTypes := make([]*redis.StatusCmd, len(allKeys))
	for i, key := range allKeys {
		keyTypes[i] = pipe.Type(r.Context(), key)
	}
	pipe.Exec(r.Context())
	result := make([]map[string]string, len(allKeys))
	for i, key := range allKeys {
		result[i] = map[string]string{"key": key, "type": keyTypes[i].Val()}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func redisValueHandler(w http.ResponseWriter, r *http.Request) {
	if rdb == nil {
		http.Error(w, "Redis not connected", 503)
		return
	}
	key := r.URL.Query().Get("key")
	dataType := r.URL.Query().Get("type")
	if key == "" || dataType == "" {
		http.Error(w, "Missing params", 400)
		return
	}
	switch r.Method {
	case "GET":
		var val interface{}
		var err error
		switch dataType {
		case "string":
			val, err = rdb.Get(r.Context(), key).Result()
		case "list":
			val, err = rdb.LRange(r.Context(), key, 0, -1).Result()
		case "hash":
			val, err = rdb.HGetAll(r.Context(), key).Result()
		default:
			http.Error(w, "Unsupported type", 400)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"key": key, "type": dataType, "value": val})
	case "POST":
		var p map[string]string
		json.NewDecoder(r.Body).Decode(&p)
		switch dataType {
		case "string":
			rdb.Set(r.Context(), key, p["value"], 0)
		case "list":
			rdb.LPush(r.Context(), key, p["value"])
		case "hash":
			rdb.HSet(r.Context(), key, p["field"], p["value"])
		}
		w.WriteHeader(201)
	case "DELETE":
		switch dataType {
		case "list":
			rdb.LRem(r.Context(), key, 1, r.URL.Query().Get("value"))
		case "hash":
			rdb.HDel(r.Context(), key, r.URL.Query().Get("field"))
		}
		w.WriteHeader(200)
	}
}

func redisKeyHandler(w http.ResponseWriter, r *http.Request) {
	if rdb == nil {
		http.Error(w, "Redis not connected", 503)
		return
	}
	if r.Method == "DELETE" {
		rdb.Del(r.Context(), r.URL.Query().Get("key"))
		w.WriteHeader(200)
	}
}

func redisInfoHandler(w http.ResponseWriter, r *http.Request) {
	if rdb == nil {
		http.Error(w, "Redis not connected", 503)
		return
	}
	info, _ := rdb.Info(r.Context(), "all").Result()
	lines := strings.Split(info, "\r\n")
	metrics := make(map[string]string)
	for _, line := range lines {
		if strings.Contains(line, ":") {
			p := strings.SplitN(line, ":", 2)
			metrics[p[0]] = p[1]
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func getDB(w http.ResponseWriter, r *http.Request, prefix string) (*sql.DB, bool) {
	dbName := strings.TrimPrefix(r.URL.Path, prefix)
	db, ok := dbConnections[dbName]
	if !ok || db == nil {
		http.Error(w, "DB not found", 503)
		return nil, false
	}
	return db, true
}

func apiMetrics(w http.ResponseWriter, r *http.Request) {
	db, ok := getDB(w, r, "/api/baseservices/mysql/metrics/")
	if !ok {
		return
	}
	var k string
	var th, maxC, openT, slowQ int
	var q int64
	var up int64
	var bufT, bufU int
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&k, &th)
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Questions'").Scan(&k, &q)
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Uptime'").Scan(&k, &up)
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Opened_tables'").Scan(&k, &openT)
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Slow_queries'").Scan(&k, &slowQ)
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Innodb_buffer_pool_pages_total'").Scan(&k, &bufT)
	db.QueryRow("SHOW GLOBAL STATUS LIKE 'Innodb_buffer_pool_pages_data'").Scan(&k, &bufU)
	db.QueryRow("SHOW VARIABLES LIKE 'max_connections'").Scan(&k, &maxC)
	now := time.Now()
	qps := 0
	qpsMutex.Lock()
	if !lastQTime.IsZero() {
		elapsed := now.Sub(lastQTime).Seconds()
		if elapsed >= 1 && q >= lastQuestions {
			qps = int(float64(q-lastQuestions) / elapsed)
		}
	}
	lastQuestions = q
	lastQTime = now
	qpsMutex.Unlock()
	uptimeStr := fmt.Sprintf("%dd %dh %dm %ds", up/86400, (up%86400)/3600, (up%3600)/60, up%60)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode([]Metric{{Time: now.Unix(), Uptime: up, UptimeStr: uptimeStr, Threads: th, QPS: qps, MaxConnections: maxC, SlowQueries: slowQ, OpenTables: openT, InnoDBBuffUsed: bufU, InnoDBBuffTotal: bufT}})
}

func apiTables(w http.ResponseWriter, r *http.Request) {
	db, ok := getDB(w, r, "/api/baseservices/mysql/tables/")
	if !ok {
		return
	}
	rows, err := db.Query(`SELECT t.table_name, IFNULL(t.table_rows,0), ROUND(IFNULL(t.data_length,0)/1024/1024), IFNULL(io.count_read,0) + IFNULL(io.count_write,0) FROM information_schema.tables t LEFT JOIN performance_schema.table_io_waits_summary_by_table io ON io.object_schema = t.table_schema AND io.object_name = t.table_name WHERE t.table_schema = DATABASE() ORDER BY 3 DESC LIMIT 10;`)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var out []TableStat
	for rows.Next() {
		var ts TableStat
		if err := rows.Scan(&ts.Name, &ts.Rows, &ts.SizeMB, &ts.Ops); err == nil {
			out = append(out, ts)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func apiProcesslist(w http.ResponseWriter, r *http.Request) {
	db, ok := getDB(w, r, "/api/baseservices/mysql/processlist/")
	if !ok {
		return
	}
	rows, err := db.Query("SHOW FULL PROCESSLIST")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	var out []ProcessListRow
	for rows.Next() {
		var id, t int
		var u, h, c, s string
		var d, i sql.NullString
		if err := rows.Scan(&id, &u, &h, &d, &c, &t, &s, &i); err == nil {
			out = append(out, ProcessListRow{Id: id, User: u, Host: h, DB: d.String, Command: c, Time: t, State: s, Info: i.String})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func apiRepl(w http.ResponseWriter, r *http.Request) {
	db, ok := getDB(w, r, "/api/baseservices/mysql/replstatus/")
	if !ok {
		return
	}
	rows, err := db.Query("SHOW SLAVE STATUS")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ReplicationStatus{Role: "master"})
		return
	}
	defer rows.Close()
	if !rows.Next() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ReplicationStatus{Role: "master"})
		return
	}
	cols, _ := rows.Columns()
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	rows.Scan(ptrs...)
	m := map[string]string{}
	for i, col := range cols {
		m[col] = vals[i].String
	}
	sb := 0
	fmt.Sscanf(m["Seconds_Behind_Master"], "%d", &sb)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ReplicationStatus{Role: "slave", SlaveRunning: (m["Slave_IO_Running"] == "Yes" && m["Slave_SQL_Running"] == "Yes"), SecondsBehind: sb})
}

func executeSQL(w http.ResponseWriter, r *http.Request) {
	db, ok := getDB(w, r, "/api/baseservices/mysql/execsql/")
	if !ok {
		return
	}
	type Req struct {
		SQL string `json:"sql"`
	}
	var req Req
	json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.SQL) == "" {
		http.Error(w, "empty", 400)
		return
	}
	rows, err := db.Query(req.SQL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SqlResult{Error: err.Error()})
		return
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var allRows [][]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		row := make([]string, len(cols))
		for i, v := range vals {
			if v.Valid {
				row[i] = v.String
			} else {
				row[i] = "NULL"
			}
		}
		allRows = append(allRows, row)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SqlResult{Columns: cols, Rows: allRows})
}

func handleIsoMount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	f, _ := w.(http.Flusher)
	fmt.Fprintf(w, ">>> Upload ISO...\n")
	f.Flush()
	r.ParseMultipartForm(10 << 30)
	file, _, err := r.FormFile("file")
	if err != nil {
		return
	}
	defer file.Close()
	dst, _ := os.Create(IsoSavePath)
	defer dst.Close()
	io.Copy(dst, file)
	mountAndConfigRepo(w, IsoSavePath)
}

func handleIsoMountLocal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	f, _ := w.(http.Flusher)
	path := r.FormValue("path")
	fmt.Fprintf(w, ">>> Checking: %s\n", path)
	f.Flush()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintf(w, "Not Found\n")
		return
	}
	mountAndConfigRepo(w, path)
}

func mountAndConfigRepo(w http.ResponseWriter, isoPath string) {
	f, _ := w.(http.Flusher)
	fmt.Fprintf(w, ">>> Mounting...\n")
	f.Flush()
	os.MkdirAll(IsoMountPoint, 0755)
	exec.Command("umount", IsoMountPoint).Run()
	if out, err := exec.Command("mount", "-o", "loop", isoPath, IsoMountPoint).CombinedOutput(); err != nil {
		fmt.Fprintf(w, "Fail: %s\n", out)
		return
	}
	os.MkdirAll(RepoBackupDir, 0755)
	exec.Command("bash", "-c", fmt.Sprintf("mv /etc/yum.repos.d/*.repo %s/", RepoBackupDir)).Run()
	rc := ""
	if _, err := os.Stat(filepath.Join(IsoMountPoint, "BaseOS")); err == nil {
		rc += fmt.Sprintf("[L-Base]\nname=Base\nbaseurl=file://%s/BaseOS\ngpgcheck=0\nenabled=1\n", IsoMountPoint)
	}
	if _, err := os.Stat(filepath.Join(IsoMountPoint, "AppStream")); err == nil {
		rc += fmt.Sprintf("[L-App]\nname=App\nbaseurl=file://%s/AppStream\ngpgcheck=0\nenabled=1\n", IsoMountPoint)
	}
	if rc == "" {
		rc = fmt.Sprintf("[L-ISO]\nname=ISO\nbaseurl=file://%s\ngpgcheck=0\nenabled=1\n", IsoMountPoint)
	}
	os.WriteFile("/etc/yum.repos.d/local.repo", []byte(rc), 0644)
	fmt.Fprintf(w, ">>> Yum makecache...\n")
	f.Flush()
	c := exec.Command("bash", "-c", "yum clean all && yum makecache")
	s, _ := c.StdoutPipe()
	c.Stderr = c.Stdout
	c.Start()
	sc := bufio.NewScanner(s)
	for sc.Scan() {
		fmt.Fprintln(w, sc.Text())
		f.Flush()
	}
	c.Wait()
	fmt.Fprintf(w, "Done.\n")
}

func handleLogDownload(w http.ResponseWriter, r *http.Request) {
	path, ok := logFileMap[r.URL.Query().Get("key")]
	if !ok {
		return
	}
	os.Chmod(path, 0644)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func handleLogWS(w http.ResponseWriter, r *http.Request) {
	path, ok := logFileMap[r.URL.Query().Get("key")]
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	if !ok {
		conn.WriteMessage(websocket.TextMessage, []byte("Bad Key"))
		return
	}
	os.Chmod(path, 0644)
	cmd := exec.Command("tail", "-f", "-n", "200", path)
	out, _ := cmd.StdoutPipe()
	cmd.Start()
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	buf := make([]byte, 4096)
	for {
		n, err := out.Read(buf)
		if err != nil {
			break
		}
		valid := strings.ToValidUTF8(string(buf[:n]), "")
		if conn.WriteMessage(1, []byte(valid)) != nil {
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
	if mkb > 0 {
		avail := getMemAvailableKB()
		if avail > 0 {
			res.SysInfo.MemUsage = float64(mkb-avail) / float64(mkb) * 100
		}
	}
	res.SysInfo.LoadAvg = getLoadAvg()
	rx, tx := getNetIO()
	res.SysInfo.NetRx = rx
	res.SysInfo.NetTx = tx
	out, _ := exec.Command("bash", "-c", "ulimit -n").Output()
	res.SysInfo.Ulimit = strings.TrimSpace(string(out))
	res.SysInfo.UlimitPass = (res.SysInfo.Ulimit != "1024")
	cmd := exec.Command("df", "-h")
	out, _ = cmd.Output()
	res.SysInfo.DiskDetail = string(out)
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || len(line) == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 6 && !strings.Contains(f[0], "tmp") && !strings.Contains(f[0], "over") {
			u, _ := strconv.Atoi(strings.TrimRight(f[4], "%"))
			res.SysInfo.DiskList = append(res.SysInfo.DiskList, DiskInfo{Mount: f[5], Total: f[1], Used: f[2], Usage: u})
		}
	}
	if o, err := exec.Command("getenforce").Output(); err == nil {
		res.SecInfo.SELinux = strings.TrimSpace(string(o))
	} else {
		res.SecInfo.SELinux = "?"
	}
	fw := "Stopped"
	if err := exec.Command("systemctl", "is-active", "firewalld").Run(); err == nil {
		fw = "Running"
	}
	res.SecInfo.Firewall = fw
	res.SecInfo.SshTunnelOk = checkSshConfig()
	if _, err := os.Stat("/opt/emm/current"); err == nil {
		res.UemInfo.Installed = true
		for _, s := range uemServices {
			st := "stop"
			if err := exec.Command("pgrep", "-f", s).Run(); err == nil {
				st = "run"
			}
			res.UemInfo.Services = append(res.UemInfo.Services, ServiceStat{Name: s, Status: st})
		}
	}
	mClient, err := minio.New(MinioEndpoint, &minio.Options{Creds: credentials.NewStaticV4(MinioUser, MinioPass, ""), Secure: false})
	if err == nil {
		exists, _ := mClient.BucketExists(context.Background(), MinioBucket)
		if exists {
			res.MinioInfo.BucketExists = true
			p, _ := mClient.GetBucketPolicy(context.Background(), MinioBucket)
			if strings.Contains(p, "GetObject") && strings.Contains(p, "*") {
				res.MinioInfo.Policy = "public"
			} else {
				res.MinioInfo.Policy = "private"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func handleFixMinio(w http.ResponseWriter, r *http.Request) {
	m, _ := minio.New(MinioEndpoint, &minio.Options{Creds: credentials.NewStaticV4(MinioUser, MinioPass, ""), Secure: false})
	p := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetBucketLocation","s3:ListBucket"],"Resource":["arn:aws:s3:::%s"]},{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, MinioBucket, MinioBucket)
	m.SetBucketPolicy(context.Background(), MinioBucket, p)
	w.Write([]byte("Done"))
}

func handleRestartService(w http.ResponseWriter, r *http.Request) {
	exec.Command("systemctl", "restart", r.URL.Query().Get("name")).Run()
	w.Write([]byte("Done"))
}

func handleFixSelinux(w http.ResponseWriter, r *http.Request) {
	exec.Command("setenforce", "0").Run()
	d, _ := os.ReadFile("/etc/selinux/config")
	os.WriteFile("/etc/selinux/config", []byte(strings.Replace(string(d), "SELINUX=enforcing", "SELINUX=disabled", 1)), 0644)
	w.Write([]byte("Done"))
}

func handleFixFirewall(w http.ResponseWriter, r *http.Request) {
	exec.Command("systemctl", "stop", "firewalld").Run()
	exec.Command("systemctl", "disable", "firewalld").Run()
	w.Write([]byte("Done"))
}

func handleFixSsh(w http.ResponseWriter, r *http.Request) {
	autoFixSshConfig()
	w.Write([]byte("Done"))
}

func autoFixSshConfig() error {
	const cfgPath = "/etc/ssh/sshd_config"
	contentBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read sshd_config failed: %w", err)
	}
	content := string(contentBytes)

	updated := false

	// 1. 如果发现 "#AllowTcpForwarding yes"，改为 "AllowTcpForwarding yes"
	if strings.Contains(content, "#AllowTcpForwarding yes") {
		content = strings.ReplaceAll(content, "#AllowTcpForwarding yes", "AllowTcpForwarding yes")
		updated = true
	}

	// 2. 如果既没有 AllowTcpForwarding yes，也没有 AllowTcpForwarding no，则追加
	if !strings.Contains(content, "AllowTcpForwarding yes") &&
		!strings.Contains(content, "AllowTcpForwarding no") {
		content += "\nAllowTcpForwarding yes\n"
		updated = true
	}

	// 没有变动，不需要写回与重启
	if !updated {
		return nil
	}

	// 3. 写回配置文件
	err = os.WriteFile(cfgPath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("write sshd_config failed: %w", err)
	}

	// 4. 重启 sshd
	if err := exec.Command("systemctl", "restart", "sshd").Run(); err != nil {
		return fmt.Errorf("restart sshd failed: %w", err)
	}

	return nil
}

func checkSshConfig() bool {
	d, _ := os.ReadFile("/etc/ssh/sshd_config")
	return strings.Contains(string(d), "AllowTcpForwarding yes")
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
	w.Header().Set("Content-Type", "text/plain")
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
	fmt.Fprintf(w, ">>> Install...\n")
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
	fmt.Fprintf(w, "Done.\n")
}

func handleDeployWS(w http.ResponseWriter, r *http.Request) {
	startPTYSession(w, r, exec.Command("/bin/bash", filepath.Join(InstallWorkDir, func() string {
		if r.URL.Query().Get("type") == "install" {
			return InstallScript
		}
		return UpdateScript
	}())))
}

func handleSysTermWS(w http.ResponseWriter, r *http.Request) {
	startPTYSession(w, r, exec.Command("/bin/bash"))
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

func getMemTotalKB() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.HasPrefix(s.Text(), "MemTotal:") {
			var k uint64
			fmt.Sscanf(strings.Fields(s.Text())[1], "%d", &k)
			return k
		}
	}
	return 0
}

func getMemAvailableKB() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if strings.HasPrefix(s.Text(), "MemAvailable:") {
			var k uint64
			fmt.Sscanf(strings.Fields(s.Text())[1], "%d", &k)
			return k
		}
	}
	return 0
}

func getLoadAvg() float64 {
	d, _ := os.ReadFile("/proc/loadavg")
	if len(d) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.Fields(string(d))[0], 64)
	return v
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

func getNetIO() (float64, float64) {
	d, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(string(d), "\n")
	var rx, tx uint64
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) < 10 {
			continue
		}
		// Basic heuristic to skip headers and loopback if needed
		if strings.Contains(f[0], ":") || len(f) > 16 {
			// Parsing can be tricky depending on spacing.
			// Usually field 1 is bytes received, field 9 is bytes transmitted
			// But if interface name is stuck to bytes (eth0:123), we need to handle it
			rStr := f[1]
			tStr := f[9]
			if strings.Contains(f[0], ":") && len(f) < 17 {
				// Format: eth0:123 456 ...
				parts := strings.Split(f[0], ":")
				if len(parts) > 1 {
					rStr = parts[1]
				}
			}
			r, _ := strconv.ParseUint(rStr, 10, 64)
			t, _ := strconv.ParseUint(tStr, 10, 64)
			rx += r
			tx += t
		}
	}
	now := time.Now()
	rRate, tRate := 0.0, 0.0
	netMutex.Lock()
	if !lastNetTime.IsZero() {
		sec := now.Sub(lastNetTime).Seconds()
		if sec > 0 {
			rRate = float64(rx-lastNetRx) / sec / 1024
			tRate = float64(tx-lastNetTx) / sec / 1024
		}
	}
	lastNetRx = rx
	lastNetTx = tx
	lastNetTime = now
	netMutex.Unlock()
	return rRate, tRate
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

// ================= 2. 前端页面 =================
const htmlPage = `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>综合运维平台</title>
    <script>
        if (!window.location.pathname.endsWith('/') && !window.location.pathname.endsWith('.html')) {
            var newUrl = window.location.protocol + "//" + window.location.host + window.location.pathname + "/" + window.location.search;
            window.history.replaceState(null, null, newUrl);
            window.location.reload(); 
        }
    </script>
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.min.css" />
    <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.4.0/css/all.min.css">
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        body { font-family: 'Segoe UI', sans-serif; background: #2c3e50; margin: 0; height: 100vh; display: flex; flex-direction: column; overflow: hidden; }
        .navbar { background: #34495e; padding: 0 20px; height: 50px; display: flex; align-items: center; border-bottom: 1px solid #1abc9c; flex-shrink: 0; }
        .brand { color: #fff; font-weight: bold; font-size: 18px; margin-right: 20px; }
        .tab-btn { background: transparent; border: none; color: #bdc3c7; font-size: 13px; padding: 0 10px; height: 100%; cursor: pointer; transition: 0.3s; border-bottom: 3px solid transparent; }
        .tab-btn:hover { color: white; background: rgba(255,255,255,0.05); }
        .tab-btn.active { color: #1abc9c; border-bottom: 3px solid #1abc9c; background: rgba(26, 188, 156, 0.1); }
        .content { flex: 1; position: relative; background: #ecf0f1; overflow: hidden; display: flex; flex-direction: column; }
        .panel { display: none; width: 100%; height: 100%; padding: 20px; box-sizing: border-box; overflow-y: auto; }
        .panel.active { display: block; }
        #panel-baseservices { padding: 0; display: none; flex-direction: column; height: 100%; overflow: hidden; }
        #panel-baseservices.active { display: flex; }
        .container-box { padding: 20px; max-width: 1200px; margin: 0 auto; width: 100%; box-sizing: border-box; }
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
        input[type="file"], input[type="text"], textarea, select { border: 1px solid #ccc; padding: 5px; background: white; font-size: 13px; border-radius: 4px; }
        .grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; }
        .grid-4 { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 20px; }
        .about-table td { padding: 10px; }
        .about-table tr:not(:last-child) td { border-bottom: 1px solid #f0f0f0; }
       .bs-header { padding: 10px 20px; background: #e9ecef; display: flex; gap: 5px; border-bottom: 1px solid #ddd; flex-shrink: 0; }
       .sub-tab-btn { background: #fff; color: #666; border: 1px solid #ddd; padding: 6px 14px; cursor: pointer; border-radius: 4px; font-size: 13px; }
       .sub-tab-btn:hover { background: #f8f9fa; }
       .sub-tab-btn.active { background: #2980b9; color: white; border-color: #2980b9; }
       .sub-panel { display: none; flex: 1; flex-direction: column; overflow: hidden; background: #fff; width: 100%; height: 100%; }
       .sub-panel.active { display: flex; }
       .modal-backdrop { position: fixed; top: 0; left: 0; width: 100%; height: 100%; background-color: rgba(0,0,0,0.5); z-index: 100; display: none; }
        .modal { position: fixed; top: 50%; left: 50%; transform: translate(-50%, -50%); background-color: #fff; padding: 25px; border-radius: 8px; box-shadow: 0 5px 15px rgba(0,0,0,0.3); z-index: 101; width: 90%; max-width: 700px; display: none; max-height: 80vh; overflow-y: auto; }
        .modal-header { display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid #dee2e6; padding-bottom: 10px; margin-bottom: 20px; }
        .modal-title { margin: 0; font-size: 1.25rem; }
        .modal-close { background: none; border: none; font-size: 1.5rem; cursor: pointer; }
        .modal-body { margin-bottom: 20px; }
        .modal-footer { border-top: 1px solid #dee2e6; padding-top: 15px; margin-top: 20px; text-align: right; }
       .list-item, .hash-item { display: flex; justify-content: space-between; align-items: center; padding: 8px; border-bottom: 1px solid #e9ecef; }
       .iframe-container { flex: 1; width: 100%; height: 100%; border: none; display: block; }
       .sql-table-container { overflow: auto; max-height: 400px; border: 1px solid #ddd; margin-top: 10px; }
       .sql-table { width: 100%; border-collapse: collapse; font-size: 13px; font-family: Consolas, monospace; white-space: nowrap; }
       .sql-table th { background: #f8f9fa; position: sticky; top: 0; border-bottom: 2px solid #ddd; padding: 8px; text-align: left; color: #333; }
       .sql-table td { border-bottom: 1px solid #eee; padding: 6px 8px; color: #444; }
       .sql-table tr:hover { background-color: #f1f1f1; }
    </style>
</head>
<body>
<div class="navbar">
    <button class="tab-btn active" onclick="switchTab('check')">🔍 操作系统</button>
    <button class="tab-btn" onclick="switchTab('deps')">🔧 环境依赖</button>
    <button class="tab-btn" onclick="switchTab('deploy')">📦 部署/更新</button>
    <button class="tab-btn" onclick="switchTab('files')">📂 文件管理</button>
    <button class="tab-btn" onclick="switchTab('terminal')">💻 终端</button>
    <button class="tab-btn" onclick="switchTab('logs')">📜 日志查看</button>
    <button class="tab-btn" onclick="switchTab('baseservices')">⚙️ 基础服务</button>
    <button class="tab-btn" onclick="switchTab('about')">ℹ️ 关于</button>
</div>
<div class="content">
    <div id="panel-check" class="panel active">
        <div class="grid-2">
            <div class="card">
                <h3>📈 系统资源 (3秒刷新)</h3>
                <div style="height: 200px; position: relative;">
                    <canvas id="sysChart"></canvas>
                </div>
            </div>
            <div class="card">
                <h3>🌐 网络流量 (KB/s)</h3>
                <div style="height: 200px; position: relative;">
                    <canvas id="netChart"></canvas>
                </div>
            </div>
        </div>
        <br>
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
    
    <div id="panel-deps" class="panel"><div class="container-box" style="max-width: 1000px;"><div class="card"><h3>💿 ISO 挂载 (配置本地 YUM)</h3><div style="display:flex; flex-direction:column; gap:10px;"><div style="display:flex; align-items:center; gap:10px;"><span style="width:80px; color:#666;">上传镜像:</span><input type="file" id="isoInput" accept=".iso" style="width:300px;"><button onclick="mountIso()">上传并挂载</button></div><div style="display:flex; align-items:center; gap:10px;"><span style="width:80px; color:#666;">本地路径:</span><input type="text" id="isoPathInput" placeholder="/tmp/kylin.iso" style="width:300px;"><button class="btn-orange" onclick="mountLocalIso()">使用本地文件</button></div></div><div id="yum-log" class="term-box" style="height:120px;margin-top:10px">等待操作...</div></div><div class="card"><h3>🛠️ RPM 安装</h3><div style="display:flex;gap:10px"><input type="file" id="rpmInput" accept=".rpm"><button onclick="installRpm()">执行安装</button></div><div id="rpm-log" class="term-box" style="height:120px;margin-top:10px"></div></div></div></div>
    
    <div id="panel-deploy" class="panel"><div class="container-box" style="max-width: 1000px;"><div class="card"><h3>📦 系统包上传</h3><div style="display:flex;gap:10px"><input type="file" id="fileInput" accept=".tar.gz"><button onclick="uploadFile()">上传解压</button><span id="uploadStatus" style="font-weight:bold"></span></div></div><div class="card" style="flex:1"><div style="display:flex;justify-content:space-between;margin-bottom:10px;align-items:center"><h3>脚本执行</h3><div style="display:flex;gap:10px"><button id="btnRunInstall" class="btn-green" onclick="startScript('install')" disabled>部署 (install.sh)</button> <button id="btnRunUpdate" class="btn-orange" onclick="startScript('update')" disabled>更新 (mdm.sh)</button></div></div><div id="deploy-term" style="height:400px;background:#000"></div></div></div></div>
    <div id="panel-files" class="panel"><div class="container-box" style="max-width: 1000px;"><div class="card" style="height:100%;padding:0"><div style="padding:15px;background:#f8f9fa;border-bottom:1px solid #eee"><div class="fm-toolbar"><button onclick="fmUpDir()">上级</button><button onclick="fmRefresh()">刷新</button><span id="fmPath" style="margin:0 10px;font-weight:bold">/root</span><input type="file" id="fmUploadInput" style="display:none" onchange="fmDoUpload()"><button onclick="document.getElementById('fmUploadInput').click()">上传</button></div><div id="fmStatus" style="font-size:12px;color:#666;height:15px"></div></div><div class="fm-list" style="overflow:auto;height:100%"><table style="width:100%"><tbody id="fmBody"></tbody></table></div></div></div></div>
    <div id="panel-terminal" class="panel"><div id="sys-term" class="full-term" style="height:100vh"></div></div>
    <div id="panel-logs" class="panel" style="padding:20px;height:100%"><div class="log-layout"><div class="log-sidebar"><div class="log-sidebar-header">日志列表</div><ul class="log-list"><li class="log-item" onclick="viewLog('tomcat', this)"><span>Tomcat</span> <button class="btn-dl-log" onclick="dlLog('tomcat', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('nginx_access', this)"><span>Nginx Access</span> <button class="btn-dl-log" onclick="dlLog('nginx_access', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('nginx_error', this)"><span>Nginx Error</span> <button class="btn-dl-log" onclick="dlLog('nginx_error', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('app_server', this)"><span>App Server</span> <button class="btn-dl-log" onclick="dlLog('app_server', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('emm_backend', this)"><span>EMM Backend</span> <button class="btn-dl-log" onclick="dlLog('emm_backend', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('license', this)"><span>License</span> <button class="btn-dl-log" onclick="dlLog('license', event)"><i class="fas fa-download"></i></button></li><li class="log-item" onclick="viewLog('platform', this)"><span>Platform</span> <button class="btn-dl-log" onclick="dlLog('platform', event)"><i class="fas fa-download"></i></button></li></ul></div><div class="log-viewer-container"><div class="log-viewer-header"><span id="logTitle">请选择...</span><div><label><input type="checkbox" id="autoScroll" checked> 自动滚动</label> <button class="btn-sm" onclick="clearLog()">清空</button></div></div><div id="logContent" class="log-content"></div></div></div></div>
    
    <div id="panel-baseservices" class="panel">
       <div class="bs-header">
           <button class="sub-tab-btn active" onclick="switchSubTab(event, 'bs-redis')">Redis</button>
           <button class="sub-tab-btn" onclick="switchSubTab(event, 'bs-mysql')">MySQL</button>
           <button class="sub-tab-btn" onclick="switchSubTab(event, 'bs-rabbitmq')">RabbitMQ</button>
           <button class="sub-tab-btn" onclick="switchSubTab(event, 'bs-minio')">MinIO</button>
       </div>
       
       <div id="bs-redis" class="sub-panel active" style="padding: 20px; overflow-y: auto;">
           <div class="container-box" style="padding:0">
             <div class="card">
                <h3>Redis 性能指标</h3>
                <div id="redis-info-grid" class="grid-4">加载中...</div>
             </div>
             <div class="card">
                <h3>键值管理</h3>
                <div id="redis-keys-table-container">加载中...</div>
             </div>
           </div>
       </div>

       <div id="bs-mysql" class="sub-panel" style="padding: 20px; overflow-y: auto;">
           <div class="container-box" style="padding:0">
             <div class="card">
                <div style="display:flex; align-items:center; gap:15px; margin-bottom:15px;">
                   <h3>MySQL 监控</h3>
                   <select id="db-selector" onchange="mysql.switchDB(this.value)"><option value="mdm">mdm</option><option value="multitenant">multitenant</option></select>
                   <button class="sub-tab-btn active" onclick="switchSubTab(event, 'mysql-monitor', false, 'mysql-tab-group')">监控</button>
                    <button class="sub-tab-btn" onclick="switchSubTab(event, 'mysql-sql', false, 'mysql-tab-group')">SQL执行</button>
                </div>
                <div id="mysql-monitor" class="mysql-tab-group active">
                   <div class="grid-4" style="margin-bottom: 15px;">
                      <div class="card"><h3>Threads</h3><div id="mysql-threads" style="font-size:1.5em;font-weight:bold;">0</div></div>
                      <div class="card"><h3>QPS</h3><div id="mysql-qps" style="font-size:1.5em;font-weight:bold;">0</div></div>
                      <div class="card"><h3>Max Connections</h3><div id="mysql-connections" style="font-size:1.5em;font-weight:bold;">0</div></div>
                      <div class="card"><h3>Uptime</h3><div id="mysql-uptime" style="font-size:1.5em;font-weight:bold;">0</div></div>
                   </div>
                   <div class="grid-2">
                      <div class="card"><h3>性能</h3><canvas id="mysql-metricChart"></canvas></div>
                      <div class="card"><h3>主从复制</h3><div id="mysql-replStatus"></div><canvas id="mysql-replChart"></canvas></div>
                      <div class="card"><h3>表空间占用 (Top 10)</h3><canvas id="mysql-tableSizeChart"></canvas></div>
                      <div class="card"><h3>频繁操作表 (Top 10)</h3><canvas id="mysql-tableOpsChart"></canvas></div>
                   </div>
                   <div class="card">
                      <h3>当前进程</h3>
                      <input id="mysql-slowFilter" placeholder="过滤SQL..." oninput="mysql.loadProcesslist()">
                       <div style="max-height: 400px; overflow-y: auto;"><table id="mysql-slowQueryTable"><thead><tr><th>Id</th><th>User</th><th>Host</th><th>DB</th><th>Command</th><th>Time(s)</th><th>State</th><th>Info</th></tr></thead><tbody></tbody></table></div>
                   </div>
                </div>
                <div id="mysql-sql" class="mysql-tab-group" style="display:none;">
                   <h3>执行SQL</h3>
                   <textarea id="mysql-sqlInput" rows="5" style="width:100%; font-family:monospace;"></textarea>
                   <button onclick="mysql.execSQL()" class="btn-green" style="margin-top:10px;">执行</button>
                   <div id="mysql-sqlResult" class="sql-table-container"></div>
                </div>
             </div>
           </div>
       </div>

       <div id="bs-rabbitmq" class="sub-panel" style="padding: 0;">
           <iframe id="frame-rabbitmq" data-src="api/baseservices/rabbitmq/" class="iframe-container"></iframe>
       </div>

       <div id="bs-minio" class="sub-panel" style="padding: 0;">
           <iframe id="frame-minio" data-src="api/baseservices/minio/" class="iframe-container"></iframe>
       </div>
    </div>

    <div id="panel-about" class="panel">
        <div class="container-box" style="max-width: 800px;">
            <div class="card">
                <h3>关于智能部署工具</h3>
                <table class="about-table">
                    <tbody>
                        <tr><td style="width: 100px;"><strong>作者</strong></td><td>王凯</td></tr>
                        <tr><td><strong>版本</strong></td><td>5.4 (Stable PTY)</td></tr>
                        <tr><td><strong>更新日期</strong></td><td>2024-07-26</td></tr>
                        <tr><td style="vertical-align: top; padding-top: 12px;"><strong>主要功能</strong></td><td><ul style="margin:0; padding-left: 20px; line-height: 1.8;"><li>系统基础环境、安全配置、服务状态一键体检</li><li><strong>新功能：实时系统资源（内存/负载/网络）监控图表</strong></li><li>通过上传或本地路径挂载 ISO 镜像，自动配置 YUM 源</li><li>在线安装 RPM 依赖包</li><li>上传部署包并执行安装/更新脚本</li><li>图形化文件管理（浏览、上传、下载）</li><li>全功能网页 Shell 终端 (Fix PTY)</li><li>实时查看多种 UEM 服务日志</li><li>基础服务(Redis/MySQL/RabbitMQ/MinIO)监控与管理</li></ul></td></tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>
</div>

<div id="modal-backdrop" class="modal-backdrop"></div>
<div id="modal" class="modal">
    <div class="modal-header"><h2 id="modal-title" class="modal-title"></h2><button id="modal-close-btn" class="modal-close">&times;</button></div>
    <div id="modal-body" class="modal-body"></div>
    <div class="modal-footer"><button type="button" id="modal-cancel-btn" class="btn-sm">关闭</button></div>
</div>

<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
<script>
    const API_BASE = "api/"; const UPLOAD_URL = "upload";
    
    let deployTerm, sysTerm, deploySocket, sysSocket, deployFit, sysFit, logSocket, currentPath = "/root";
    let sysChart, netChart; let checkInterval;

    window.onload = function() { initCharts(); runCheck(); fmLoadPath("/root"); startCheckPolling(); }
    function startCheckPolling() { if(checkInterval) clearInterval(checkInterval); checkInterval = setInterval(() => { if(document.getElementById('panel-check').classList.contains('active')) { runCheck(); } }, 3000); }
    function initCharts() {
        const ctx = document.getElementById('sysChart').getContext('2d');
        sysChart = new Chart(ctx, { type: 'line', data: { labels: [], datasets: [ { label: '内存使用率 (%)', data: [], borderColor: '#e74c3c', backgroundColor: 'rgba(231, 76, 60, 0.1)', fill: true, tension: 0.3 }, { label: '系统负载 (1min) - CPU活跃进程', data: [], borderColor: '#2980b9', backgroundColor: 'rgba(41, 128, 185, 0.1)', fill: true, tension: 0.3, yAxisID: 'y1' } ] }, options: { responsive: true, maintainAspectRatio: false, animation: false, interaction: { mode: 'index', intersect: false, }, scales: { y: { beginAtZero: true, max: 100, title: { display: true, text: 'Memory %' } }, y1: { type: 'linear', display: true, position: 'right', beginAtZero: true, title: { display: true, text: 'Load Avg' }, grid: { drawOnChartArea: false, }, }, x: { ticks: { display: false } } } } });
        const ctx2 = document.getElementById('netChart').getContext('2d');
        netChart = new Chart(ctx2, { type: 'line', data: { labels: [], datasets: [ { label: 'Rx (下载)', data: [], borderColor: '#27ae60', fill: false, tension: 0.3 }, { label: 'Tx (上传)', data: [], borderColor: '#f39c12', fill: false, tension: 0.3 } ] }, options: { responsive: true, maintainAspectRatio: false, animation: false, scales: { y: { beginAtZero: true, title: { display: true, text: 'KB/s' } }, x: { ticks: { display: false } } } } });
    }
    function switchTab(id) {
        document.querySelectorAll('.panel').forEach(p => p.classList.remove('active')); document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        document.getElementById('panel-'+id).classList.add('active'); event.target.classList.add('active');
        if (id === 'terminal') { if (!sysTerm) initSysTerm(); setTimeout(()=>sysFit.fit(), 200); }
        if (id === 'deploy') { setTimeout(()=>deployFit && deployFit.fit(), 200); }
        if (id === 'baseservices') { redis.init(); mysql.init(); }
    }
    function switchSubTab(event, id, isLink, group) {
       if (isLink) { document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active')); const mainBtn = Array.from(document.querySelectorAll('.tab-btn')).find(b => b.textContent.includes('基础服务')); if(mainBtn) mainBtn.classList.add('active'); document.querySelectorAll('.panel').forEach(p => p.classList.remove('active')); document.getElementById('panel-baseservices').classList.add('active'); }
       if(group) { const p = event.target.closest('.card'); p.querySelectorAll('.'+group).forEach(x=>x.style.display='none'); p.querySelectorAll('.sub-tab-btn').forEach(b=>b.classList.remove('active')); document.getElementById(id).style.display='block'; event.target.classList.add('active'); return; } 
       else { const parent = document.getElementById('panel-baseservices'); parent.querySelectorAll('.sub-panel').forEach(p => p.classList.remove('active')); parent.querySelectorAll('.sub-tab-btn').forEach(b => b.classList.remove('active')); document.getElementById(id).classList.add('active'); event.target.classList.add('active'); }
       if (id === 'bs-rabbitmq') { const frame = document.getElementById('frame-rabbitmq'); if (!frame.src) frame.src = frame.dataset.src; } 
       else if (id === 'bs-minio') { const frame = document.getElementById('frame-minio'); if (!frame.src) { frame.src = frame.dataset.src; frame.onload = function() { let attempts = 0; const interval = setInterval(() => { attempts++; if(attempts > 40) clearInterval(interval); try { const doc = frame.contentWindow.document; const user = doc.getElementById('accessKey'); const pass = doc.getElementById('secretKey'); const btn = doc.querySelector('button[type="submit"]'); if(user && pass && btn) { const nativeInputValueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set; nativeInputValueSetter.call(user, 'admin'); user.dispatchEvent(new Event('input', { bubbles: true })); nativeInputValueSetter.call(pass, 'Nqsky1130'); pass.dispatchEvent(new Event('input', { bubbles: true })); setTimeout(() => { btn.click(); }, 300); clearInterval(interval); } } catch(e) {} }, 500); }; } }
    }
    function getWsUrl(ep) { let path = location.pathname; if (!path.endsWith('/')) path += '/'; return (location.protocol==='https:'?'wss://':'ws://') + location.host + path + ep; }
    function viewLog(key, el) {
        document.querySelectorAll('.log-item').forEach(l=>l.classList.remove('active')); el.classList.add('active'); document.getElementById('logTitle').innerText = "Log: " + key;
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
            if(sysChart && data.sys_info.mem_usage !== undefined) {
                const now = new Date().toLocaleTimeString();
                if(sysChart.data.labels.length > 20) { sysChart.data.labels.shift(); sysChart.data.datasets.forEach(d => d.data.shift()); netChart.data.labels.shift(); netChart.data.datasets.forEach(d => d.data.shift()); }
                sysChart.data.labels.push(now); sysChart.data.datasets[0].data.push(data.sys_info.mem_usage); sysChart.data.datasets[1].data.push(data.sys_info.load_avg); sysChart.update();
                netChart.data.labels.push(now); netChart.data.datasets[0].data.push(data.sys_info.net_rx || 0); netChart.data.datasets[1].data.push(data.sys_info.net_tx || 0); netChart.update();
            }
            let baseHtml = '';
            baseHtml += row('CPU', data.sys_info.cpu_cores + ' 核', data.sys_info.cpu_pass); baseHtml += row('内存', data.sys_info.mem_total, data.sys_info.mem_pass); baseHtml += row('架构', data.sys_info.arch, true); baseHtml += row('操作系统', data.sys_info.os_name, data.sys_info.os_pass);
            baseHtml += '<tr><td>性能(ulimit)</td><td>'+data.sys_info.ulimit+'</td><td>'+(data.sys_info.ulimit_pass?'<span class="pass">OK</span>':'<span class="warn">Opt</span>')+'</td></tr>';
            document.getElementById('baseTable').innerHTML = baseHtml;
            let secHtml = '';
            secHtml += '<tr><td>SELinux</td><td>'+data.sec_info.selinux+'</td><td>'+(data.sec_info.selinux==="Disabled"||data.sec_info.selinux==="Permissive"?'<span class="pass">OK</span>':'<button class="btn-sm btn-fix" onclick="fixSelinux()">⛔ 关闭</button>')+'</td></tr>';
            
            // Firewall Logic Fix
            let fwStatus = data.sec_info.firewall; // Backend returns "Running" or "Stopped"
            let fwDisplay = '';
            if (fwStatus === 'Stopped' || fwStatus === 'Off') {
                fwDisplay = '<span class="pass">OK</span>';
            } else {
                fwDisplay = '<button class="btn-sm btn-fix" onclick="fixFirewall()">⛔ 关闭</button>';
            }
            secHtml += '<tr><td>防火墙</td><td>'+fwStatus+'</td><td>'+fwDisplay+'</td></tr>';

            let sshBtn = data.sec_info.ssh_tunnel_ok ? '<span class="pass">开启</span>' : '<span class="fail">关闭</span> <button class="btn-sm btn-fix" onclick="fixSsh()">🔧 修复</button>';
            secHtml += '<tr><td>SSH隧道</td><td>TCP转发</td><td>'+sshBtn+'</td></tr>';
            document.getElementById('secTable').innerHTML = secHtml;
            let diskHtml = '<div style="display:flex; flex-direction:column; gap:12px;">';
            data.sys_info.disk_list.forEach(d => { let color = d.usage>=90?'bg-red':(d.usage>=75?'bg-orange':'bg-green'); diskHtml += '<div><div style="font-weight:bold;margin-bottom:4px;font-size:13px;">'+d.mount+' <span style="color:#666">('+d.usage+'%)</span></div><div class="progress-bg"><div class="progress-bar '+color+'" style="width:'+d.usage+'%"></div></div><div class="disk-text"><span>'+d.used+'</span><span>'+d.total+'</span></div></div>'; });
            document.getElementById('diskList').innerHTML = diskHtml + '</div>';
            const uemBox = document.getElementById('uemStatusBox');
            if (!data.uem_info.installed) { uemBox.innerHTML = '<div style="color:#7f8c8d;text-align:center;padding:20px;">未检测到 UEM</div>'; } 
            else { let h = '<table style="width:100%"><thead><tr><th>服务</th><th>状态</th><th>操作</th></tr></thead><tbody>'; data.uem_info.services.forEach(s => { let st = s.status==='running'?'<span class="pass">Run</span>':'<span class="fail">Stop</span>'; h += '<tr><td>'+s.name+'</td><td>'+st+'</td><td><button class="btn-sm btn-restart" onclick="restartService(\''+s.name+'\')">重启</button></td></tr>'; }); uemBox.innerHTML = h + '</tbody></table>'; }
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
    async function fmLoadPath(p) { currentPath=p; document.getElementById('fmPath').innerText=p; const r=await fetch(API_BASE+'fs/list?path='+encodeURIComponent(p)); const fs=await r.json(); let h=''; fs.sort((a,b)=>(a.is_dir===b.is_dir)?0:a.is_dir?-1:1); fs.forEach(f=>{ let n=f.is_dir?'<a class="link-dir" href="javascript:fmLoadPath(\''+f.path+'\')">'+f.name+'</a>':f.name; let act=f.is_dir?'':'<button class="btn-sm" onclick="fmDownload(\''+f.path+'\')">下载</button>'; h+='<tr><td>'+(f.is_dir?'📁':'📄')+' '+n+'</td><td>'+f.size+'</td><td>'+f.mod_time+'</td><td>'+act+'</td></tr>'; }); document.getElementById('fmBody').innerHTML=h; }
    function fmUpDir() { let p=currentPath.split('/'); p.pop(); let n=p.join('/'); if(!n)n='/'; fmLoadPath(n); }
    function fmDownload(p) { window.location.href = API_BASE + 'fs/download?path=' + encodeURIComponent(p); }
    async function fmDoUpload() { const inp=document.getElementById('fmUploadInput'); const fd=new FormData(); fd.append("file", inp.files[0]); fd.append("path", currentPath); const st=document.getElementById('fmStatus'); st.innerText="Uploading..."; await fetch(API_BASE+'upload_any', {method:'POST', body:fd}); st.innerText="Done"; fmLoadPath(currentPath); }
    async function mountIso() { const inp=document.getElementById('isoInput'); if(!inp.files.length)return; event.target.disabled=true; const fd=new FormData(); fd.append("file",inp.files[0]); const r=await fetch(API_BASE+'iso_mount',{method:'POST',body:fd}); const rd=r.body.getReader(); const d=new TextDecoder(); const box=document.getElementById('yum-log'); while(true){const{done,value}=await rd.read();if(done)break;box.innerText+=d.decode(value);box.scrollTop=box.scrollHeight;} event.target.disabled=false; }
    async function mountLocalIso() { const p = document.getElementById('isoPathInput').value; if(!p) return alert("请输入路径"); event.target.disabled=true; const fd=new FormData(); fd.append("path", p); const r=await fetch(API_BASE+'iso_mount_local',{method:'POST',body:fd}); const rd=r.body.getReader(); const d=new TextDecoder(); const box=document.getElementById('yum-log'); box.innerText = ">>> 正在使用本地文件挂载...\n"; while(true){const{done,value}=await rd.read();if(done)break;box.innerText+=d.decode(value);box.scrollTop=box.scrollHeight;} event.target.disabled=false; }
    async function installRpm() { const i=document.getElementById('rpmInput'); if(!i.files.length)return; event.target.disabled=true; const fd=new FormData(); fd.append("file",i.files[0]); const r=await fetch(API_BASE+'rpm_install',{method:'POST',body:fd}); const rd=r.body.getReader(); const d=new TextDecoder(); const box=document.getElementById('rpm-log'); while(true){const{done,value}=await rd.read();if(done)break;box.innerText+=d.decode(value);box.scrollTop=box.scrollHeight;} event.target.disabled=false; }
    async function uploadFile() { const i=document.getElementById('fileInput'); if(!i.files.length)return; event.target.disabled=true; const fd=new FormData(); fd.append("file", i.files[0]); try { const r=await fetch(UPLOAD_URL, {method:'POST', body:fd}); if(r.ok) { document.getElementById('uploadStatus').innerHTML = "<span class='pass'>✅ 成功</span>"; document.getElementById('btnRunInstall').disabled=false; document.getElementById('btnRunUpdate').disabled=false; } else { throw await r.text(); } } catch(e){alert("Error: "+e);} event.target.disabled=false; }
    function startScript(type) { if(deployTerm) deployTerm.dispose(); if(deploySocket) deploySocket.close(); deployTerm=new Terminal({cursorBlink:true,fontSize:13,theme:{background:'#000'}}); deployFit=new FitAddon.FitAddon(); deployTerm.loadAddon(deployFit); deployTerm.open(document.getElementById('deploy-term')); deployFit.fit(); deploySocket=new WebSocket(getWsUrl("ws/deploy?type="+type)); setupSocket(deploySocket, deployTerm, deployFit); document.getElementById('btnRunInstall').disabled=true; document.getElementById('btnRunUpdate').disabled=true; }
    function initSysTerm() { sysTerm=new Terminal({cursorBlink:true,fontSize:14,fontFamily:'Consolas, monospace'}); sysFit=new FitAddon.FitAddon(); sysTerm.loadAddon(sysFit); sysTerm.open(document.getElementById('sys-term')); sysFit.fit(); sysSocket=new WebSocket(getWsUrl("ws/terminal")); setupSocket(sysSocket, sysTerm, sysFit); }
    function setupSocket(s, t, f) { s.onopen=()=>{s.send(JSON.stringify({type:"resize",cols:t.cols,rows:t.rows}));f.fit()}; s.onmessage=e=>t.write(e.data); t.onData(d=>{if(s.readyState===1)s.send(JSON.stringify({type:"input",data:d}))}); window.addEventListener('resize',()=>{f.fit();if(s.readyState===1)s.send(JSON.stringify({type:"resize",cols:t.cols,rows:t.rows}))}); }
    function escapeHtml(unsafe) { return unsafe ? unsafe.toString().replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;") : ''; }

    const redis = {
       allKeys: [], currentFilter: 'all', initialized: false,
       init: function() { if(this.initialized) return; this.fetchInfo(); this.fetchAllKeys(); this.initialized = true; },
       fetchInfo: async function() { try { const res = await fetch(API_BASE + 'baseservices/redis/info'); if (!res.ok) throw new Error('Failed to fetch info'); const info = await res.json(); const metrics = {'redis_version': 'Version', 'uptime_in_days': 'Uptime (Days)', 'connected_clients': 'Clients', 'used_memory_human': 'Memory', 'total_commands_processed': 'Commands', 'instantaneous_ops_per_sec': 'Ops/Sec'}; const grid = document.getElementById('redis-info-grid'); grid.innerHTML = ''; for (const key in metrics) { if (info[key]) grid.innerHTML += '<div class="card"><h3>' + metrics[key] + '</h3><p style="font-size:1.5em;font-weight:bold;">' + info[key] + '</p></div>'; } } catch (e) { document.getElementById('redis-info-grid').innerHTML = '<p class="fail">Failed to load Redis stats.</p>'; } },
       fetchAllKeys: async function() { try { const res = await fetch(API_BASE + 'baseservices/redis/keys'); if (!res.ok) throw new Error('Failed to fetch keys'); this.allKeys = await res.json() || []; this.allKeys.sort((a, b) => a.key.localeCompare(b.key)); this.renderTable(); } catch (e) { document.getElementById('redis-keys-table-container').innerHTML = '<p class="fail">Failed to load keys.</p>'; } },
       renderTable: function() { let html = '<table><thead><tr><th>Key</th><th>Type</th><th>Actions</th></tr></thead><tbody>'; this.allKeys.forEach(item => { html += '<tr><td title="' + escapeHtml(item.key) + '">' + escapeHtml(item.key) + '</td><td>' + escapeHtml(item.type) + '</td><td><button class="btn-sm" onclick="redis.viewEditKey(\'' + item.key + '\', \'' + item.type + '\')">View/Edit</button> <button class="btn-sm btn-red" onclick="redis.deleteKey(\'' + item.key + '\')">Delete</button></td></tr>'; }); html += '</tbody></table>'; document.getElementById('redis-keys-table-container').innerHTML = html; },
       deleteKey: async function(key) { if (!confirm('确认删除: ' + key + '?')) return; await fetch(API_BASE + 'baseservices/redis/key?key=' + encodeURIComponent(key), { method: 'DELETE' }); this.fetchAllKeys(); },
       viewEditKey: async function(key, type) { document.getElementById('modal-title').textContent = 'Editing ' + type + ': ' + key; document.getElementById('modal-body').innerHTML = '<p>Loading...</p>'; document.getElementById('modal-backdrop').style.display = 'block'; document.getElementById('modal').style.display = 'block'; const res = await fetch(API_BASE + 'baseservices/redis/value?type=' + type + '&key=' + encodeURIComponent(key)); const data = await res.json(); this.renderModalContent(data); },
       renderModalContent: function(data) {
          let body = '';
          switch (data.type) {
             case 'string': body = '<div class="form-group"><label>Value</label><textarea id="stringValue" rows="5" style="width:100%">' + escapeHtml(data.value) + '</textarea></div><button class="btn-green" onclick="redis.saveStringValue(\'' + data.key + '\')">Save</button>'; break;
             case 'list': let items = data.value.map(item => '<div class="list-item"><span>' + escapeHtml(item) + '</span><button class="btn-sm btn-red" onclick="redis.deleteListItem(\'' + data.key + '\', \'' + escapeHtml(item) + '\')">Delete</button></div>').join(''); body = '<div class="form-group"><input type="text" id="newListItem" placeholder="New Item" style="width:100%"><button class="btn-green" style="margin-top:10px;" onclick="redis.addListItem(\'' + data.key + '\')">Add</button></div><hr>' + items; break;
             case 'hash': let fields = Object.entries(data.value).map(([f, v]) => '<div class="hash-item"><span><strong>' + escapeHtml(f) + ':</strong> ' + escapeHtml(v) + '</span><button class="btn-sm btn-red" onclick="redis.deleteHashField(\'' + data.key + '\', \'' + escapeHtml(f) + '\')">Delete</button></div>').join(''); body = '<div class="form-group"><input type="text" id="newHashField" placeholder="Field" style="width:100%"><textarea id="newHashValue" placeholder="Value" style="width:100%"></textarea><button class="btn-green" style="margin-top:10px;" onclick="redis.addHashField(\'' + data.key + '\')">Save</button></div><hr>' + fields; break;
             default: body = '<p>Unsupported type: ' + data.type + '</p>';
          }
          document.getElementById('modal-body').innerHTML = body;
       },
       saveStringValue: async function(key) { const value = document.getElementById('stringValue').value; await fetch(API_BASE + 'baseservices/redis/value?type=string&key=' + encodeURIComponent(key), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) }); this.hideModal(); },
       addListItem: async function(key) { const value = document.getElementById('newListItem').value; if (!value) return; await fetch(API_BASE + 'baseservices/redis/value?type=list&key=' + encodeURIComponent(key), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ value }) }); this.viewEditKey(key, 'list'); },
       deleteListItem: async function(key, value) { await fetch(API_BASE + 'baseservices/redis/value?type=list&key=' + encodeURIComponent(key) + '&value=' + encodeURIComponent(value), { method: 'DELETE' }); this.viewEditKey(key, 'list'); },
       addHashField: async function(key) { const field = document.getElementById('newHashField').value; const value = document.getElementById('newHashValue').value; if (!field) return; await fetch(API_BASE + 'baseservices/redis/value?type=hash&key=' + encodeURIComponent(key), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ field, value }) }); this.viewEditKey(key, 'hash'); },
       deleteHashField: async function(key, field) { await fetch(API_BASE + 'baseservices/redis/value?type=hash&key=' + encodeURIComponent(key) + '&field=' + encodeURIComponent(field), { method: 'DELETE' }); this.viewEditKey(key, 'hash'); },
       hideModal: function() { document.getElementById('modal-backdrop').style.display = 'none'; document.getElementById('modal').style.display = 'none'; }
    };
    document.getElementById('modal-close-btn').addEventListener('click', () => redis.hideModal());
    document.getElementById('modal-cancel-btn').addEventListener('click', () => redis.hideModal());
    document.getElementById('modal-backdrop').addEventListener('click', () => redis.hideModal());

    const mysql = {
       currentDB: 'mdm', initialized: false, charts: {},
       init: function() {
          if(this.initialized) return;
          this.charts.metric = new Chart(document.getElementById('mysql-metricChart').getContext('2d'), { type: 'line', data: { labels: [], datasets: [{ label: 'Threads', data: [], borderColor: '#2980b9', fill: false }, { label: 'QPS', data: [], borderColor: '#27ae60', fill: false }] }, options: { responsive: true, animation: false } });
          this.charts.size = new Chart(document.getElementById('mysql-tableSizeChart').getContext('2d'), { type: 'bar', data: { labels: [], datasets: [{ label: 'Size MB', data: [], backgroundColor: 'rgba(52, 152, 219, 0.6)' }] }, options: { responsive: true, indexAxis: 'y' } });
          this.charts.ops = new Chart(document.getElementById('mysql-tableOpsChart').getContext('2d'), { type: 'bar', data: { labels: [], datasets: [{ label: 'Ops', data: [], backgroundColor: 'rgba(231, 76, 60, 0.6)' }] }, options: { responsive: true, indexAxis: 'y' } });
          this.charts.repl = new Chart(document.getElementById('mysql-replChart').getContext('2d'), { type: 'line', data: { labels: [], datasets: [{ label: 'Delay(s)', data: [], borderColor: '#c0392b', fill: false }] }, options: { responsive: true, animation: false } });
          this.loadAll(); setInterval(() => this.loadAll(), 10000); this.initialized = true;
       },
       switchDB: function(db) { this.currentDB = db; this.loadAll(); },
       loadAll: async function() { await Promise.all([ this.loadMetrics(), this.loadTables(), this.loadProcesslist(), this.loadRepl() ]); },
       loadMetrics: async function() { try { const res = await fetch(API_BASE + 'baseservices/mysql/metrics/' + this.currentDB); const arr = await res.json(); if (!arr || arr.length === 0) return; const m = arr[0]; document.getElementById('mysql-threads').innerText = m.threads; document.getElementById('mysql-qps').innerText = m.qps; document.getElementById('mysql-connections').innerText = m.max_connections; document.getElementById('mysql-uptime').innerText = m.uptime_str; const now = new Date().toLocaleTimeString(); if (this.charts.metric.data.labels.length > 20) { this.charts.metric.data.labels.shift(); this.charts.metric.data.datasets.forEach(ds => ds.data.shift()); } this.charts.metric.data.labels.push(now); this.charts.metric.data.datasets[0].data.push(m.threads); this.charts.metric.data.datasets[1].data.push(m.qps); this.charts.metric.update(); } catch (e) { console.error('mysql.loadMetrics', e); } },
       loadTables: async function() { try { const res = await fetch(API_BASE + 'baseservices/mysql/tables/' + this.currentDB); const data = await res.json(); if (!Array.isArray(data)) return; this.charts.size.data.labels = data.map(d => d.name); this.charts.size.data.datasets[0].data = data.map(d => d.size_mb); this.charts.size.update(); this.charts.ops.data.labels = data.map(d => d.name); this.charts.ops.data.datasets[0].data = data.map(d => d.ops); this.charts.ops.update(); } catch (e) { console.error('mysql.loadTables', e); } },
       loadProcesslist: async function() { try { const res = await fetch(API_BASE + 'baseservices/mysql/processlist/' + this.currentDB); const data = await res.json(); const filter = document.getElementById('mysql-slowFilter').value.toLowerCase(); const tbody = document.querySelector('#mysql-slowQueryTable tbody'); tbody.innerHTML = ''; (data || []).forEach(q => { if (filter && (!q.info || !q.info.toLowerCase().includes(filter))) return; tbody.innerHTML += '<tr><td>' + q.id + '</td><td>' + q.user + '</td><td>' + q.host + '</td><td>' + q.db + '</td><td>' + q.command + '</td><td>' + q.time + '</td><td>' + q.state + '</td><td>' + escapeHtml(q.info) + '</td></tr>'; }); } catch (e) { console.error('mysql.loadProcesslist', e); } },
       loadRepl: async function() { try { const res = await fetch(API_BASE + 'baseservices/mysql/replstatus/' + this.currentDB); const r = await res.json(); document.getElementById('mysql-replStatus').innerHTML = 'Role: ' + r.role + ' | Slave Running: <span class="' + (r.slave_running ? 'pass' : 'fail') + '">' + r.slave_running + '</span> | Delay(s): ' + r.seconds_behind; if (this.charts.repl.data.labels.length > 20) { this.charts.repl.data.labels.shift(); this.charts.repl.data.datasets[0].data.shift(); } this.charts.repl.data.labels.push(new Date().toLocaleTimeString()); this.charts.repl.data.datasets[0].data.push(r.seconds_behind || 0); this.charts.repl.update(); } catch (e) { console.error('mysql.loadRepl', e); } },
       execSQL: async function() { const sql = document.getElementById('mysql-sqlInput').value.trim(); if (!sql) return; const res = await fetch(API_BASE + 'baseservices/mysql/execsql/' + this.currentDB, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ sql }) }); const result = await res.json(); const div = document.getElementById('mysql-sqlResult'); if(result.error) { div.innerHTML = '<div style="color:red; padding:10px;">Error: ' + escapeHtml(result.error) + '</div>'; return; } if(!result.columns || result.columns.length === 0) { div.innerHTML = '<div style="padding:10px; color:#666;">Query executed successfully. No rows returned.</div>'; return; } let tableHtml = '<table class="sql-table"><thead><tr>'; result.columns.forEach(col => { tableHtml += '<th>' + escapeHtml(col) + '</th>'; }); tableHtml += '</tr></thead><tbody>'; if(result.rows) { result.rows.forEach(row => { tableHtml += '<tr>'; row.forEach(cell => { tableHtml += '<td>' + escapeHtml(cell) + '</td>'; }); tableHtml += '</tr>'; }); } tableHtml += '</tbody></table>'; div.innerHTML = tableHtml; }
    };
</script>
</body>
</html>
`
