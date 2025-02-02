package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/jmuk/groupcache"
	"github.com/jmuk/groupcache/k8s"
	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

func main() {
	klog.Infof("starting")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	restClient, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}

	serviceName := os.Getenv("SERVICE_NAME")
	if serviceName == "" {
		panic("SERVICE_NAME is not set")
	}
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		panic("NAMESPACE is not set")
	}
	self := os.Getenv("SELF")
	if self == "" {
		panic("SELF is not set")
	}
	gcPortStr := os.Getenv("GROUPCACHE_PORT")
	if gcPortStr == "" {
		panic("GROUPCACHE_PORT is not set")
	}
	gcPort, err := strconv.ParseInt(gcPortStr, 10, 32)
	if err != nil {
		panic(err)
	}
	portStr := os.Getenv("HTTP_PORT")
	if portStr == "" {
		panic("HTTP_PORT is not set")
	}

	m, err := k8s.NewPeersManager(
		ctx,
		kubernetes.NewForConfigOrDie(restClient),
		serviceName,
		namespace,
		int(gcPort),
		fmt.Sprintf("%s:%d", self, gcPort),
	)
	if err != nil {
		panic(err)
	}
	defer m.Stop()

	var g *groupcache.Group
	getter := groupcache.GetterFunc(func(ctx context.Context, key string, sink groupcache.Sink) error {
		klog.Infof("self: %s, key: %s", self, key)
		keyInt, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return err
		}
		var result int64
		if keyInt <= 1 {
			result = keyInt
		} else {
			eg, ctx := errgroup.WithContext(ctx)
			for i := int64(1); i <= 2; i++ {
				nk := keyInt - i
				eg.Go(func() error {
					var s string
					if err := g.Get(ctx, strconv.FormatInt(nk, 10), groupcache.StringSink(&s)); err != nil {
						return err
					}
					v, err := strconv.ParseInt(s, 10, 64)
					if err != nil {
						return err
					}
					atomic.AddInt64(&result, v)
					return nil
				})
			}
			if err := eg.Wait(); err != nil {
				return err
			}
		}
		klog.Infof("key: %s, value: %d", key, result)
		return sink.SetString(strconv.FormatInt(result, 10))
	})
	g = groupcache.NewGroup("fib", 1024*1024, getter)
	http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("q")
		if key == "" {
			http.Error(w, "q is not specified", http.StatusInternalServerError)
			return
		}
		var v string
		err := g.Get(r.Context(), key, groupcache.StringSink(&v))
		if err != nil {
			http.Error(w, "failed to obtain the result", http.StatusInternalServerError)
			return
		}
		w.Header().Add("content-type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(v))
	}))
	err = http.ListenAndServe(":"+portStr, nil)
	panic(err)
}
