package store

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

type RedisStore struct {
	client *redis.Client
	ctx    context.Context
}

func (s *RedisStore) Initialize() {
	s.ctx = context.Background()
	s.client = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", config.Current.Store.Redis.Host, config.Current.Store.Redis.Port),
		Username: config.Current.Store.Redis.Username,
		Password: config.Current.Store.Redis.Password,
		DB:       config.Current.Store.Redis.Database,
	})

	log.Debug().Msg("Trying to reach redis...")
	status := s.client.Ping(s.ctx)
	if err := status.Err(); err != nil {
		log.Fatal().Err(err).Msg("Could not reach redis!")
	}

	log.Info().Msg("Redis connection established...")

	for _, cmd := range config.Current.Store.Redis.InitCommands {
		log.Debug().Fields(map[string]any{
			"command": cmd,
		}).Msg("Executing init command")

		args := utils.AsAnySlice(strings.Split(cmd, " "))
		if err := s.client.Do(s.ctx, args...).Err(); err != nil {
			if err.Error() != "Index already exists" {
				log.Warn().Err(err).Msg("Could not executed init command!")
			}
		}
	}
}

func (s *RedisStore) OnAdd(obj *unstructured.Unstructured) {
	var status = s.client.JSONSet(s.ctx, obj.GetName(), ".", obj.Object)
	if err := status.Err(); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not write resource to store!")
	}
}

func (s *RedisStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	var status = s.client.JSONSet(s.ctx, oldObj.GetName(), ".", newObj)
	if err := status.Err(); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(newObj)).Err(err).Msg("Could not update resource in store!")
	}
}

func (s *RedisStore) OnDelete(obj *unstructured.Unstructured) {
	var status = s.client.JSONDel(s.ctx, obj.GetName(), ".")
	if err := status.Err(); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not delete resource from store!")
	}
}
