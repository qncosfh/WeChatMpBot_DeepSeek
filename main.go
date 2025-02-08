package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"io/ioutil"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type WeChatMessage struct {
	ToUserName   string `xml:"ToUserName"`
	FromUserName string `xml:"FromUserName"`
	CreateTime   int64  `xml:"CreateTime"`
	MsgType      string `xml:"MsgType"`
	Content      string `xml:"Content"`
	Event        string `xml:"Event"`
}

type DeepSeekResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

var userResponses sync.Map // 缓存用户的 DeepSeek 结果

func initConfig() {
	viper.SetConfigFile("config.yaml")
	if err := viper.ReadInConfig(); err != nil {
		log.Println("⚠️ 加载配置文件失败:", err)
	} else {
		log.Println("✅ 配置文件加载成功")
	}
}

func checkSignature(signature, timestamp, nonce string) bool {
	token := viper.GetString("wechat.token")
	if token == "" || timestamp == "" || nonce == "" {
		log.Println("❌ wechat.token 或参数为空，签名校验失败")
		return false
	}

	strs := []string{token, timestamp, nonce}
	sort.Strings(strs)

	hash := sha1.New()
	hash.Write([]byte(strings.Join(strs, "")))
	sha1Hash := fmt.Sprintf("%x", hash.Sum(nil))

	log.Printf("🔍 计算签名: sha1(%s) = %s, 传入的 signature = %s", strings.Join(strs, ""), sha1Hash, signature)

	return sha1Hash == signature
}

func main() {
	initConfig()
	r := gin.Default()

	// 微信验证接口
	r.GET("/wx", func(c *gin.Context) {
		signature := c.Query("signature")
		timestamp := c.Query("timestamp")
		nonce := c.Query("nonce")
		echostr := c.Query("echostr")

		if checkSignature(signature, timestamp, nonce) {
			c.String(http.StatusOK, echostr)
		} else {
			c.String(http.StatusForbidden, "Forbidden")
		}
	})

	// 微信消息处理接口
	r.POST("/wx", handleMessage)

	log.Println("✅ Server started on port 80")
	r.Run(":80")
}

func handleMessage(c *gin.Context) {
	var msg WeChatMessage
	if err := c.ShouldBindXML(&msg); err != nil {
		log.Printf("❌ XML 解析失败: %v", err)
		c.String(http.StatusBadRequest, "Bad Request")
		return
	}

	var response string

	switch msg.MsgType {
	//触发关注事件后自动回复
	case "event":
		if msg.Event == "subscribe" {
			response = "👻 感谢您的关注！\n本公众号接入了 DeepSeek，你可以直接向我提问。"
		} else {
			response = "📢 事件已收到，但未做特殊处理。"
		}
	//接受到文本消息
	case "text":
		// 用户查询 DeepSeek 结果
		if strings.TrimSpace(msg.Content) == "继续" {
			if cachedResponse, ok := userResponses.Load(msg.FromUserName); ok {
				response = cachedResponse.(string)
				userResponses.Delete(msg.FromUserName) // 取出后删除，避免缓存积累
			} else {
				response = "⌛ 目前没有待查看的回答，请先输入问题。"
			}
		} else {
			// 异步调用 DeepSeek
			go fetchDeepSeekResponse(msg.FromUserName, msg.Content)
			time.Sleep(time.Second * 3)
			response = "⏳ 处理中，请输入“继续”查看答案。"
		}
	default:
		response = "📸 内容已收到，但当前不支持。"
	}

	reply := fmt.Sprintf(`<xml>
		<ToUserName><![CDATA[%s]]></ToUserName>
		<FromUserName><![CDATA[%s]]></FromUserName>
		<CreateTime>%d</CreateTime>
		<MsgType><![CDATA[text]]></MsgType>
		<Content><![CDATA[%s]]></Content>
	</xml>`, msg.FromUserName, msg.ToUserName, time.Now().Unix(), response)

	c.Data(http.StatusOK, "application/xml", []byte(reply))
}

// 异步调用 DeepSeek 并缓存结果
func fetchDeepSeekResponse(user string, query string) {
	response, err := callDeepSeek(query)
	if err != nil {
		log.Printf("❌ DeepSeek 调用失败: %v", err)
		response = "❌ DeepSeek 处理失败，请稍后再试。"
	}
	userResponses.Store(user, response) // 缓存结果，供用户输入“继续”查询
}

// 调用 DeepSeek API
func callDeepSeek(query string) (string, error) {
	url := viper.GetString("deepseek.api_url")
	apiKey := viper.GetString("deepseek.api_key")
	prompt := viper.GetString("deepseek.prompt")

	payload := map[string]interface{}{
		"model": viper.GetString("deepseek.model"),
		"messages": []map[string]string{
			{"role": "system", "content": prompt},
			{"role": "user", "content": query},
		},
		"stream": false,
	}

	payloadBytes, _ := json.Marshal(payload)
	log.Println("🔵 DeepSeek 请求 JSON:", string(payloadBytes))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	log.Println("🟢 DeepSeek API 响应:", string(body))

	var deepSeekResp DeepSeekResponse
	if err := json.Unmarshal(body, &deepSeekResp); err != nil {
		return "", err
	}

	if len(deepSeekResp.Choices) > 0 {
		return deepSeekResp.Choices[0].Message.Content, nil
	}

	return "⚠️ No response from DeepSeek", nil
}
