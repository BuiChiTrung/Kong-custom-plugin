package main

import (
	"fmt"
	"github.com/BuiChiTrung/kong-custom-plugin/kong/logger"
	"github.com/Kong/go-pdk"
	"github.com/Kong/go-pdk/server"
	"github.com/redis/go-redis/v9"
	"net/http"
	"strconv"
)

type Config struct {
	TTLSeconds       uint
	ErrTTLSeconds    uint
	Headers          []string
	DisableNormalize bool

	TurnOffRedis bool
}

var gKong *pdk.PDK
var gConf Config
var gSvc *Service

func New() interface{} {
	gConf = Config{}

	var err error
	gSvc, err = NewService()
	if err != nil {
		panic(err)
	}

	logger.InitializeDefaultZapLogger()

	return &gConf
}

func (c Config) Access(kong *pdk.PDK) {
	// TODO: trung.bc - TD
	_ = kong.ServiceRequest.ClearHeader(HeaderAcceptEncoding)

	gKong = kong
	cacheKey, shouldCached, err := c.GenerateCacheKey(kong)
	if err != nil {
		logger.Error(err.Error())
		return
	}
	if !shouldCached {
		_ = kong.Response.SetHeader(HeaderXCacheStatus, string(Bypass))
		return
	}

	logger.Debugf("cachekey: %s, shouldcached: %b, err: %v", cacheKey, shouldCached, err)

	if err := kong.Ctx.SetShared(CacheKey, cacheKey); err != nil {
		logger.Errorf("err set shared context: %v", err)
		return
	}

	cacheVal, err := gSvc.GetCacheKey(cacheKey)
	logger.Debugf("cacheval: %s, err: %v", cacheVal, err)
	if err != nil {
		_ = kong.Response.SetHeader(HeaderXCacheStatus, string(Miss))
		if err == redis.Nil {
			_ = kong.Response.SetHeader(HeaderXCacheKey, cacheKey)
			return
		}
		logger.Errorf("error get redis key: %v", err)
	} else {
		_ = kong.Ctx.SetShared(ResponseAlreadyCached, true)

		kong.Response.Exit(200, cacheVal, map[string][]string{
			HeaderContentType:              {"application/json"},
			HeaderXCacheKey:                {cacheKey},
			HeaderXCacheStatus:             {string(Hit)},
			HeaderAccessControlAllowOrigin: {"*"},
		})
	}
}

func (c Config) GenerateCacheKey(kong *pdk.PDK) (cacheKey string, shouldCached bool, err error) {
	requestBody, err := kong.Request.GetRawBody()
	if err != nil {
		return "", false, fmt.Errorf("err get request body: %w", err)
	}

	var requestHeader string
	for _, header := range c.Headers {
		headerContent, _ := kong.Request.GetHeader(header)
		requestHeader += headerContent
	}

	requestPath, err := kong.Request.GetPath()
	if err != nil {
		return "", false, fmt.Errorf("err GenerateCacheKey get req path")
	}

	cacheKey, shouldCached, err = gSvc.GenerateCacheKey(string(requestBody), requestHeader, requestPath)
	if err != nil {
		return "", false, err
	}

	return cacheKey, shouldCached, nil
}

// Response automatically enables the buffered proxy mode.
func (c Config) Response(kong *pdk.PDK) {
}

func (c Config) Log(kong *pdk.PDK) {
	isCache, err := kong.Ctx.GetSharedAny(ResponseAlreadyCached)
	if v, ok := isCache.(bool); ok && v {
		return
	}

	responseBody, err := kong.ServiceResponse.GetRawBody()
	if err != nil {
		logger.Errorf("error get service response: %v", err)
		return
	}

	cacheKey, err := kong.Ctx.GetSharedString(CacheKey)
	if err != nil {
		logger.Errorf("err get shared context: %v", err.Error())
	}
	if cacheKey == "" {
		return
	}

	c.InsertCacheKey(kong, cacheKey, responseBody)
}

func (c Config) InsertCacheKey(kong *pdk.PDK, cacheKey string, cacheValue string) {
	logger.Infof("Insert Cache-key: %s", cacheKey)

	statusCode, _ := kong.ServiceResponse.GetStatus()
	if statusCode >= http.StatusInternalServerError {
		return
	}

	if statusCode >= http.StatusBadRequest && c.ErrTTLSeconds > 0 {
		if err := gSvc.InsertCacheKey(cacheKey, cacheValue, int64(c.ErrTTLSeconds)*int64(NanoSecond)); err != nil {
			logger.Errorf("err set redis key: %v", err)
		}
		return
	}

	var ttlSeconds uint

	ttlHeaderStr, _ := kong.Request.GetHeader(TTLSeconds)
	ttlHeader, err := strconv.Atoi(ttlHeaderStr)

	if ttlHeaderNotProvideOrInvalid := err != nil; ttlHeaderNotProvideOrInvalid {
		ttlSeconds = c.TTLSeconds
	} else if clientDonWantToCache := ttlHeader < 0; clientDonWantToCache {
		return
	} else {
		ttlSeconds = uint(ttlHeader)
	}

	if err := gSvc.InsertCacheKey(cacheKey, cacheValue, int64(ttlSeconds)*int64(NanoSecond)); err != nil {
		logger.Errorf("error set redis key: %v", err)
	}
}

func main() {
	Version := "1.1"
	Priority := 1
	_ = server.StartServer(New, Version, Priority)
}
