package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"bufio"
	"regexp"
	"strings"

	"crypto/tls"
	"github.com/fsnotify/fsnotify"
	// "golang.org/x/sys/unix"
)

// 修改默认监听的事件类型， fsnotify.go 433行 defaultOpts
// 事件结构体
type FileEvent struct {
	EventType    string `json:"event_type"`
	FilePath     string `json:"file_path"`
	MoveFromPath string `json:"move_from_path"`
	date         time.Time
}


var (
	prefixes []string          // 需要排除的文件名前缀列表
	suffixes []string          // 需要排除的文件名后缀列表
	regexps  []*regexp.Regexp  // 需要排除的正则表达式列表
	fileEventMap map[string]FileEvent // 用来CloseWrite事件的存储文件路径和它最后触发时间的映射
	django_url = "http://127.0.0.1:80/smb_active"
	watch_dir = ""
)



// InitExcludes 初始化排除规则，从文件中加载前缀、后缀和正则表达式
func InitExcludes() error {
	// 1. 加载前缀规则
	if err := loadLines("prefix.txt", &prefixes); err != nil {
		return fmt.Errorf("加载 prefix.txt 失败: %v", err)
	}

	// 2. 加载后缀规则
	if err := loadLines("suffix.txt", &suffixes); err != nil {
		return fmt.Errorf("加载 suffix.txt 失败: %v", err)
	}

	// 3. 加载正则表达式规则
	if err := loadRegexps("reg.txt"); err != nil {
		return fmt.Errorf("加载 reg.txt 失败: %v", err)
	}

	return nil
}

// loadLines 从文件中加载每一行内容到指定的字符串切片中
func loadLines(filename string, lines *[]string) error {
	file, err := os.Open(filename)
	if err != nil {
		// 如果文件不存在，忽略错误（视为无规则）
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			// 忽略空行和注释行（以 # 开头）
			continue
		}
		*lines = append(*lines, line)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

// loadRegexps 从文件中加载每一行作为正则表达式，并编译为 *regexp.Regexp 对象
func loadRegexps(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		// 如果文件不存在，忽略错误（视为无规则）
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			// 忽略空行和注释行（以 # 开头）
			continue
		}

		re, err := regexp.Compile(line)
		if err != nil {
			return fmt.Errorf("编译正则表达式失败: %s, 错误: %v", line, err)
		}
		regexps = append(regexps, re)
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

// ShouldExclude 判断给定路径是否应该被排除（是临时文件或文件夹）
func ShouldExclude(path string) bool {
	// 获取文件名（不含路径）
	filename := filepath.Base(path)

	// 规则 1：检查是否匹配前缀规则
	for _, prefix := range prefixes {
		if strings.HasPrefix(filename, prefix) {
			return true
		}
	}

	// 规则 2：检查是否匹配后缀规则
	for _, suffix := range suffixes {
		if strings.HasSuffix(filename, suffix) {
			return true
		}
	}

	// 规则 3：检查是否匹配正则表达式规则（对完整路径进行匹配）
	for _, re := range regexps {
		if re.MatchString(path) {
			fmt.Println("[debug] regexp compare:", re.String())
			return true
		}
	}

	// 默认不排除
	return false
}

// 初始化文件事件映射
func init() {
	fileEventMap = make(map[string]FileEvent)
	// 获取当前目录
	currentDir, err := os.Getwd()
	if err != nil {
		log.Fatal("Error getting current directory:", err)
	}
	watch_dir = filepath.Join(currentDir, "knowledge_base", "smb")

	// 初始化排除规则
	if err := InitExcludes(); err != nil {
		fmt.Printf("初始化排除规则失败: %v\n", err)
		return
	}


}

func main() {

	// 定义一个命令行参数 -s
	sFlag := flag.String("s", "", "django server api url")
	pFlag := flag.String("p", "", "watch dir")

	// 解析命令行参数
	flag.Parse()

	// 如果命令行参数 -s 被设置，则将其值赋给全局变量
	if *sFlag != "" {
		django_url = *sFlag
	}

	if *pFlag != "" {
		watch_dir = *pFlag
	}

	fmt.Printf("[+] 开始监听路径: %s, 提交url: %s\n", watch_dir, django_url)

	// 创建 fsnotify 监视器
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("Error creating watcher:", err)
	}
	defer watcher.Close()

	// 递归监控目标目录
	err = watchDir(watcher, watch_dir)
	if err != nil {
		log.Fatal("Error watching directory:", err)
	}

	// 启动事件监听
	go func() {
		for {
			select {
			case event := <-watcher.Events:
				// 处理WRITE/create/remove事件
				//fmt.Println("event number:", event.Op)
				fmt.Println("[event]", event)
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.CloseWrite) {
					if event.Has(fsnotify.Create) {
						is_dir, err := isDir(event.Name)
						if err != nil {
							continue
						}
						if is_dir {
							fmt.Println("[debug] add dir:", event.Name)
							// 添加监控
							err = watcher.Add(event.Name)
							if err != nil {
								log.Println("Error adding watcher to subdirectory:", event.Name)
							}
							continue
						} else {
							handleFileEvent(watcher, event)
						}

					} else {
						handleFileEvent(watcher, event)
					}
				}
			case err := <-watcher.Errors:
				log.Println("Error:", err)
			}
		}
	}()

	// 启动一个后台协程，定期检查文件事件map
	go monitorFileEvents()

	// 阻塞，防止程序退出
	select {}
}

func isDir(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fileInfo.IsDir(), nil
}

// 递归监控目录
func watchDir(watcher *fsnotify.Watcher, dir string) error {
	// 监视目标目录的文件变化
	err := watcher.Add(dir)
	if err != nil {
		return fmt.Errorf("Error adding watcher to directory %s: %v", dir, err)
	}

	// 遍历并递归监控子目录
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Println("Error accessing path:", err)
			return err
		}
		if info.IsDir() {
			// 跳过监控当前目录
			if path == dir {
				return nil
			}
			// 监控子目录
			err := watcher.Add(path)
			if err != nil {
				log.Println("Error adding watcher to subdirectory:", path)
			}
		}
		return nil
	})

	return err
}

// 处理WRITE事件，更新文件的时间戳
func handleFileEvent(w *fsnotify.Watcher, event fsnotify.Event) {
	// 更新或添加文件到map，记录当前的时间，仅记录Write事件
	// fmt.Println("事件类型：", event.Op)

	////文件写入
	//if event.Has(fsnotify.Write) {
	//	fileEventMap[event.Name] = FileEvent{
	//		EventType:    "write",
	//		FilePath:     event.Name,
	//		MoveFromPath: "",
	//		date:         time.Now(),
	//	}
	//} else if event.Has(fsnotify.Remove) {
	//	//文件删除
	//	fileEventMap[event.Name] = FileEvent{
	//		EventType:    "remove",
	//		FilePath:     event.Name,
	//		MoveFromPath: "",
	//		date:         time.Now(),
	//	}
	//
	//} else if event.Has(fsnotify.Create) {
	//	//文件创建/重命名/移动
	//	fmt.Println("文件创建自：", event.GetMoveFrom())
	//	fileEventMap[event.Name] = FileEvent{
	//		EventType:    "create",
	//		FilePath:     event.Name,
	//		MoveFromPath: event.GetMoveFrom(),
	//		date:         time.Now(),
	//	}
	//}

	if ShouldExclude(event.Name) {
		fmt.Println("[debug] do not sent file:", event.Name)
		return
	}

	e := FileEvent{
		EventType:    "",
		FilePath:     event.Name,
		MoveFromPath: "",
		date:         time.Now(),
	}
	if event.Has(fsnotify.CloseWrite) || event.Has(fsnotify.Remove) {
		if event.Has(fsnotify.CloseWrite) {
			e.EventType = "write"
			fileEventMap[event.Name] = e
		} else {
			e.EventType = "remove"
			go post_file_active(e)
		}
	} else if event.Has(fsnotify.Create) {
		if event.GetMoveFrom() != "" {
			moveFrom := event.GetMoveFrom()

			if ShouldExclude(moveFrom) {
				fmt.Println("[debug] do not sent file:", moveFrom)
				return
			}
			e.EventType = "create"
			e.MoveFromPath = moveFrom
			go post_file_active(e)
		}
	}

	//
}

func post_file_active(event FileEvent) {
	eventData, err := json.Marshal(event)
	if err != nil {
		log.Println("Error marshalling event to JSON:", err)
		return
	}

	// 创建HTTPS客户端，忽略证书错误
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 忽略证书错误
			},
		},
		Timeout: 20 * time.Second, // 设置请求超时
	}

	// 创建 POST 请求
	resp, err := client.Post(django_url, "application/json", bytes.NewBuffer(eventData))
	if err != nil {
		log.Println("Error sending HTTP request:", err)
		return
	}
	defer resp.Body.Close()

	// 输出响应状态码
	if resp.StatusCode == http.StatusOK {
		fmt.Println("[+] Event successfully sent to API:", event.FilePath)
	} else {
		fmt.Printf("[-] Failed to send event. Status code: %d\n", resp.StatusCode)
	}
}

// 后台协程，定期检查map中的文件，超时则发送请求
func monitorFileEvents() {
	for {
		// 获取当前时间
		now := time.Now()
		var keysToDelete []string
		// 遍历map中的所有文件
		for filePath, saveEvent := range fileEventMap {
			// 如果文件的时间戳已经超过5秒
			if now.Sub(saveEvent.date) > 5*time.Second {
				fmt.Println("[发送] 事件：", saveEvent)
				// 发送事件信息到API
				//fileEvent := FileEvent{
				//	EventType: "WRITE",
				//	FilePath:  filePath,
				//}
				keysToDelete = append(keysToDelete, filePath)
				go func() {
					post_file_active(saveEvent)
					// 从map中移除已经处理的文件
				}()
				time.Sleep(time.Millisecond * 300)
			}
		}

		//在for循环结束后，再去删除数组中的元素，否则会导致问题
		for _, key := range keysToDelete {
			delete(fileEventMap, key)
		}

		// 每6秒钟检查一次
		time.Sleep(5 * time.Second)
	}
}
