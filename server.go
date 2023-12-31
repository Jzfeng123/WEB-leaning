package Geektutu_learning

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//	func indexHandler(w http.ResponseWriter, req *http.Request) {
//		fmt.Fprintf(w, "欢迎啊， 处理器为:indexhandler, 路由为%q\n", req.URL.Path)
//	}
//
//	func helloHandler(w http.ResponseWriter, req *http.Request) {
//		fmt.Fprintf(w, "欢迎啊， 处理器为:hellohandler, 路由为%q\n", req.URL.Path)
//	}
//
//	func main() {
//		http.HandleFunc("/", indexHandler)
//		http.HandleFunc("/hello", helloHandler)
//		if err := http.ListenAndServe(":8080", nil); err != nil {
//			log.Fatal(err)
//		}
//	}
/*
type Handler interface {
	ServeHTTP(ResponseWriter, *Request)
}
*/
// 视图函数签名，到时候可以封装成上下文的形式
// 这种方式会导致无法扩展，因此需要用上下文来进行抽象
//type HandleFunc func(w http.ResponseWriter, req *http.Request) //抽象一个处理函数
type HandleFunc func(*Context)

type server interface {
	http.Handler
	Start(addr string) error
	Stop() error
	// 注册路由，一个非常核心的API，不能给开发者乱用
	// 造一些衍生API给开发者使用
	addRouter(method string, path string, handleFunc HandleFunc, middlewareChains ...MiddlewareHandleFunc)
}

// MiddlewareChains 中间件责任链
type MiddlewareChains []MiddlewareHandleFunc

var _ server = &HTTPServer{} // 代码层面判断有没有实现HTTPServer这个接口

/*
	一个Server需要的功能是

开启和关闭,这是为了兼容性，因为不能就只写http，后续可能还有https
*/
// Option设计模式
type HTTPOption func(h *HTTPServer)

type HTTPServer struct {
	srv  *http.Server
	stop func() error
	//routers 临时存在路由的位置
	//router map[string]HandleFunc
	// 前缀路由树
	router *router
	// *router 和 router *router的区别：前者是直接嵌套，当前结构体直接通过结构体调用对象中的方法，后者是组装，如果想要通过当前结构体调用对象方法
	//需要使用h.router.addRoute()
	// 路由组
	// 这是一个根路由组需要初始化
	*RouterGroup
	// 维护整个项目的路由组
	groups []*RouterGroup
}

/*
{
	"GET-login": HandleFunc1,
	"POST-login": HandleFunc2,

}
*/

func WithHTTPServerStop(fn func() error) HTTPOption {
	return func(h *HTTPServer) {
		if fn == nil { //实现一个优雅关闭逻辑
			fn = func() error {
				fmt.Println("程序正常启动")
				quit := make(chan os.Signal)
				signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
				<-quit // 阻塞住
				log.Println("Shutdown Server ...")

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// 关闭之前需要做某些操作
				if err := h.srv.Shutdown(ctx); err != nil {
					log.Fatal("Server Shutdown", err)
				}
				// 关闭之后需要做的操作
				select {
				case <-ctx.Done():
					log.Println("timeout of 5 seconds")
				}
				return nil
			}
		}
		h.stop = fn

	}
}
func NewHTTP(opts ...HTTPOption) *HTTPServer {
	routerGroup := NewGroup() //初始化一个路由组
	h := &HTTPServer{
		router:      newRouter(),
		RouterGroup: routerGroup,
	}
	// 结构体相互嵌套的初始化过程
	routerGroup.engine = h
	//
	//h.RouterGroup = &RouterGroup{
	//	engine: h,
	//}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// 接收请求转发请求
// 接收前端传过来的请求
// 转发请求：转发前端发过来的请求到咱们的框架中
// ServerHTTP向前对接前端请求，向后对接框架
// 前端发请求给ServerHTTP， 后端处理后直接根据这个方法发送给前端
func (h *HTTPServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 1.匹配路由
	fmt.Printf("req is %s\n", req.URL.Path)
	node, params, ok := h.router.getRouter(req.Method, req.URL.Path) //用户传入的路由从这里获取node
	if !ok || node.handleFunc == nil {                               // 返回false表示路由匹配失败
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 NOT FOUND\n"))
		return
	}
	// 2.构造上下文
	ctx := newContext(w, req)
	ctx.params = params //将每一个动态路由的结果保存到上下文中, 一个路由对应一个上下文
	fmt.Printf("ServerHTTP add router %s - %s\n", ctx.Method, ctx.Pattern)
	// 将项目全局中间件注册好
	//mids := []MiddlewareHandleFunc{Logger()}
	mids := h.filterMiddlewares(ctx.Pattern) // 搜集当前请求路由中的所有中间件的方法
	//if len(mids) == 0 { //如果没当前请求没有中间件那我们就创一个空的
	//mids = make([]MiddlewareHandleFunc, 0) //这样做的目的是为了统一执行，每一个下标对应一个路由的handler和中间件
	//}
	//mids = append(mids, node.middlewareChain...)
	gms := h.filterMiddlewares(ctx.Pattern)
	if len(gms) != 0 {
		gms = append(gms, mids...)
	}
	handleFunc := node.handleFunc
	// 构造责任链制度的核心
	for i := len(mids) - 1; i >= 0; i-- { //遍历完每一个中间件后开始执行相应的视图函数及中间件逻辑
		handleFunc = mids[i](handleFunc) // mids[i] --> func(next middlewareHandlerFunc) HandlerFunc, 所以我们需要去执行相应的视图函数
	}
	handleFunc(ctx)     //执行每一个请求的处理器
	ctx.FlashToHeader() // 将响应数据写入响应体中。
	// 第一版
	//key := ctx.Method + "-" + ctx.Pattern
	//if handler, ok := h.router[key]; ok { // 如果对应的key存在handler
	//	handler(ctx) //转发请求
	//} else {
	//	w.WriteHeader(http.StatusNotFound)
	//	_, _ = w.Write([]byte("404 NOT FOUND\n"))
}

// filterMiddlewares 匹配当前URL所对应的所有中间件, 首先engine里面要获取所有的中间件
func (h *HTTPServer) filterMiddlewares(pattern string) []MiddlewareHandleFunc {
	// pattern 是一个login静态路由而不是路由组里面应该如何去匹配。
	mids := make([]MiddlewareHandleFunc, 0)
	for _, group := range h.groups {
		if strings.HasPrefix(pattern, group.prefix) { // 与""匹配都是true
			// pattern = "/login/jzf"
			/*
				prefix = "", middlewares:[mid1, mid2] //没有注册Group的时候就是这一种情况
				prefix = "/v1", middlewares:[mid1, mid2]
				prefix = "/v2", middlewares:[mid1, mid2]
			*/
			mids = append(mids, group.middlewares...)
		}
	}
	return mids
}

/*
中间件放在每个路由组之中，但是我们在HTTPServer中拿不到所有的路由组信息,
所以我们应该在engine身上维护整个项目的路由组，如果我们可以获取所有的路由组那么我们就可以获取所有的中间件
*/

// Start 启动服务
func (h *HTTPServer) Start(addr string) error {
	h.srv = &http.Server{
		Addr:    addr,
		Handler: h,
	}

	return h.srv.ListenAndServe()
}

// 停止服务
func (h *HTTPServer) Stop() error {
	return h.stop()
}

// 注册路由的时机：项目启动的时候，后续就不能注册路由了
// 注册路由放在哪里？--->有前缀树放前缀树，没前缀树先放map里面，实现一个静态路由匹配
//func (h *HTTPServer) addRouter(method string, pattern string, handleFunc HandleFunc) {
//	key := method + "-" + pattern                        // "GET-login" 目的是要唯一
//	fmt.Printf("add router %s - %s \n", method, pattern) // method表示的是方法GET PUT DELETE AND POST，
//	//pattern表示自定义的匹配格式
//	h.router[key] = handleFunc //注册完毕, 每个路由对应一个HandleFunc
//}

//func Login(c *Context) {
//	fmt.Printf("Login请求成功, %s-%s \n", c.Pattern, c.req.URL.Path)
//	_, _ = c.w.Write([]byte("Login 请求成功\n"))
//}
//
//func Register(c *Context) { // 对应的处理器
//	_, _ = c.w.Write([]byte("Register 请求成功\n"))
//}
//func main() {
//	h := NewHTTP()
//	h.GET("/login/jzf", Login)
//	h.GET("/login/jzf", Login)
//	//h.GET("/", Login)
//	h.POST("/register", Register)
//	err := h.Start(":8888")
//	if err != nil {
//		panic(err)
//	}
//}
