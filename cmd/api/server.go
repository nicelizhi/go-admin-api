package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nicelizhi/go-admin-core/sdk/runtime"
	"golang.org/x/text/language"

	ginI18n "github.com/gin-contrib/i18n"
	"github.com/gin-gonic/gin"
	"github.com/nicelizhi/go-admin-core/config/source/file"
	"github.com/nicelizhi/go-admin-core/sdk"
	"github.com/nicelizhi/go-admin-core/sdk/api"
	"github.com/nicelizhi/go-admin-core/sdk/config"
	"github.com/nicelizhi/go-admin-core/sdk/pkg"
	"github.com/spf13/cobra"

	resource "go-admin/admin-ui"
	"go-admin/app/admin/models"
	"go-admin/app/admin/router"
	"go-admin/app/jobs"
	"go-admin/common/database"
	"go-admin/common/global"
	common "go-admin/common/middleware"
	"go-admin/common/middleware/handler"
	"go-admin/common/storage"
	ext "go-admin/config"
)

var (
	configYml string
	apiCheck  bool
	StartCmd  = &cobra.Command{
		Use:          "server",
		Short:        "Start API server",
		Example:      "go-admin server -c config/settings.yml",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			setup()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}
)

var AppRouters = make([]func(), 0)

func init() {
	StartCmd.PersistentFlags().StringVarP(&configYml, "config", "c", "config/settings.yml", "Start server with provided configuration file")
	StartCmd.PersistentFlags().BoolVarP(&apiCheck, "api", "a", false, "Start server with check api data")

	//注册路由 fixme 其他应用的路由，在本目录新建文件放在init方法
	AppRouters = append(AppRouters, router.InitRouter)

	// config the timezone
	os.Setenv("TZ", config.ApplicationConfig.TimeZone)
}

func setup() {
	// 注入配置扩展项
	config.ExtendConfig = &ext.ExtConfig
	//1. 读取配置
	config.Setup(
		file.NewSource(file.WithPath(configYml)),
		database.Setup,
		storage.Setup,
	)
	//注册监听函数
	queue := sdk.Runtime.GetMemoryQueue("")
	queue.Register(global.LoginLog, models.SaveLoginLog)
	queue.Register(global.OperateLog, models.SaveOperaLog)
	queue.Register(global.ApiCheck, models.SaveSysApi)
	go queue.Run()

	usageStr := `starting api server...`
	log.Println(usageStr)
}

func run() error {
	if config.ApplicationConfig.Mode == pkg.ModeProd.String() {
		gin.SetMode(gin.ReleaseMode)
	}
	initRouter()

	for _, f := range AppRouters {
		f()
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.ApplicationConfig.Host, config.ApplicationConfig.Port),
		Handler: sdk.Runtime.GetEngine(),
	}

	go func() {
		jobs.InitJob()
		jobs.Setup(sdk.Runtime.GetDb())

	}()

	if apiCheck {
		var routers = sdk.Runtime.GetRouter()
		q := sdk.Runtime.GetMemoryQueue("")
		mp := make(map[string]interface{}, 0)
		mp["List"] = routers
		message, err := sdk.Runtime.GetStreamMessage("", global.ApiCheck, mp)
		if err != nil {
			log.Printf("GetStreamMessage error, %s \n", err.Error())
			//日志报错错误，不中断请求
		} else {
			err = q.Append(message)
			if err != nil {
				log.Printf("Append message error, %s \n", err.Error())
			}
		}
	}

	go func() {
		// 服务连接
		if config.SslConfig.Enable {
			if err := srv.ListenAndServeTLS(config.SslConfig.Pem, config.SslConfig.KeyStr); err != nil && err != http.ErrServerClosed {
				log.Fatal("listen: ", err)
			}
		} else {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatal("listen: ", err)
			}
		}
	}()
	fmt.Println(pkg.Red(string(global.LogoContent)))
	tip()
	fmt.Println(pkg.Green("Server run at:"))
	fmt.Printf("-  Local:   %s://localhost:%d/ \r\n", "http", config.ApplicationConfig.Port)
	fmt.Printf("-  Network: %s://%s:%d/ \r\n", pkg.GetLocaHonst(), "http", config.ApplicationConfig.Port)
	fmt.Println(pkg.Green("Swagger run at:"))
	fmt.Printf("-  Local:   http://localhost:%d/swagger/admin/index.html \r\n", config.ApplicationConfig.Port)
	fmt.Printf("-  Network: %s://%s:%d/swagger/admin/index.html \r\n", "http", pkg.GetLocaHonst(), config.ApplicationConfig.Port)
	fmt.Printf("%s Enter Control + C Shutdown Server \r\n", pkg.GetCurrentTimeStr())
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fmt.Printf("%s Shutdown Server ... \r\n", pkg.GetCurrentTimeStr())

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server Shutdown:", err)
	}
	select {
	case <-ctx.Done():
		log.Println("timeout of 5 seconds")
	}
	log.Println("Server exiting")

	return nil
}

var Router runtime.Router

func tip() {
	usageStr := `Welcome ` + pkg.Green(`go-admin api `+global.Version) + ` you can use ` + pkg.Red(`-h`) + ` to view help`
	fmt.Printf("%s \n\n", usageStr)
	systemStr := `Server timezone is ` + pkg.Green(config.ApplicationConfig.TimeZone) + ` Local is` + pkg.Red(config.ApplicationConfig.Locale)
	fmt.Printf("%s \n\n", systemStr)
}

func initRouter() {
	var r *gin.Engine
	h := sdk.Runtime.GetEngine()
	if h == nil {
		h = gin.New()
		sdk.Runtime.SetEngine(h)
	}
	switch h.(type) {
	case *gin.Engine:
		r = h.(*gin.Engine)
	default:
		log.Fatal("not support other engine")
		//os.Exit(-1)
	}
	if config.SslConfig.Enable {
		r.Use(handler.TlsHandler())
	}
	//r.Use(middleware.Metrics())
	//r.StaticFS("/admin-ui", http.FS(f))
	r.Use(common.Sentinel()).
		Use(common.RequestId(pkg.TrafficKey)).
		Use(api.SetRequestLogger).
		Use(ginI18n.Localize(ginI18n.WithBundle(&ginI18n.BundleCfg{
			RootPath:         "./lang/localizeJSON",
			AcceptLanguage:   []language.Tag{language.English, language.Chinese},
			DefaultLanguage:  language.English,
			UnmarshalFunc:    json.Unmarshal,
			FormatBundleFile: "json",
		})))

	r.NoRoute(func(ctx *gin.Context) {
		accept := ctx.Request.Header.Get("Accept")
		flag := strings.Contains(accept, "text/html")
		if flag {
			ctx.Writer.WriteHeader(200)
			ctx.Writer.WriteString(string(resource.Html))
			ctx.Writer.Flush()
		}
	})

	common.InitMiddleware(r)

}
