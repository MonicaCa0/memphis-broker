package server

import (
	"context"
	"encoding/json"
	"errors"
	"memphis-broker/models"
	"memphis-broker/notifications"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var UI_url string

const CONN_STATUS_SUBJ = "$memphis_connection_status"
const INTEGRATIONS_UPDATES_SUBJ = "$memphis_integration_updates"
const NOTIFICATION_EVENTS_SUBJ = "$memphis_notifications"

func (s *Server) ListenForZombieConnCheckRequests() error {
	_, err := s.subscribeOnGlobalAcc(CONN_STATUS_SUBJ, CONN_STATUS_SUBJ+"_sid", func(_ *client, subject, reply string, msg []byte) {
		go func(msg []byte) {
			message := strings.TrimSuffix(string(msg), "\r\n")
			reported := checkAndReportConnFound(s, message, reply)

			if !reported {
				maxIterations := 14
				for range time.Tick(time.Second * 2) {
					reported = checkAndReportConnFound(s, message, reply)
					if reported {
						return
					}
					maxIterations--
					if maxIterations == 0 {
						return
					}
				}
			}
		}(copyBytes(msg))
	})
	if err != nil {
		return err
	}
	return nil
}

func checkAndReportConnFound(s *Server, message, reply string) bool {
	connInfo := &ConnzOptions{}
	conns, _ := s.Connz(connInfo)
	for _, conn := range conns.Conns {
		connId := strings.Split(conn.Name, "::")[0]
		if connId == message {
			s.sendInternalAccountMsgWithReply(s.GlobalAccount(), reply, _EMPTY_, nil, []byte("connExists"), true)
			return true
		}
	}
	return false
}

func (s *Server) ListenForIntegrationsUpdateEvents() error {
	_, err := s.subscribeOnGlobalAcc(INTEGRATIONS_UPDATES_SUBJ, INTEGRATIONS_UPDATES_SUBJ+"_sid"+s.Name(), func(_ *client, subject, reply string, msg []byte) {
		go func(msg []byte) {
			var integrationUpdate models.CreateIntegrationSchema
			err := json.Unmarshal(msg, &integrationUpdate)
			if err != nil {
				s.Errorf(err.Error())
			}
			systemKeysCollection.UpdateOne(context.TODO(), bson.M{"key": "ui_url"},
				bson.M{"$set": bson.M{"value": integrationUpdate.UIUrl}})
			switch strings.ToLower(integrationUpdate.Name) {
			case "slack":
				notifications.CacheSlackDetails(integrationUpdate.Keys, integrationUpdate.Properties)
			default:
				return
			}
		}(copyBytes(msg))
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) ListenForNotificationEvents() error {
	err := s.queueSubscribe(NOTIFICATION_EVENTS_SUBJ, NOTIFICATION_EVENTS_SUBJ+"_group", func(_ *client, subject, reply string, msg []byte) {
		go func(msg []byte) {
			var notification models.Notification
			err := json.Unmarshal(msg, &notification)
			if err != nil {
				return
			}
			notificationMsg := notification.Msg
			if notification.Code != "" {
				notificationMsg = notificationMsg + "\n```" + notification.Code + "```"
			}
			err = notifications.SendNotification(notification.Title, notificationMsg, notification.Type)
			if err != nil {
				return
			}
		}(copyBytes(msg))
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) StartBackgroundTasks() error {
	s.ListenForPoisonMessages()
	err := s.ListenForZombieConnCheckRequests()
	if err != nil {
		return errors.New("Failed subscribing for zombie conns check requests: " + err.Error())
	}

	err = s.ListenForIntegrationsUpdateEvents()
	if err != nil {
		return errors.New("Failed subscribing for integrations updates: " + err.Error())
	}

	err = s.ListenForNotificationEvents()
	if err != nil {
		return errors.New("Failed subscribing for schema validation updates: " + err.Error())
	}
	filter := bson.M{"key": "ui_url"}
	var systemKey models.SystemKey
	err = systemKeysCollection.FindOne(context.TODO(), filter).Decode(&systemKey)
	if err == mongo.ErrNoDocuments {
		UI_url = ""
		uiUrlKey := models.SystemKey{
			ID:    primitive.NewObjectID(),
			Key:   "ui_url",
			Value: "",
		}

		_, err = systemKeysCollection.InsertOne(context.TODO(), uiUrlKey)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		UI_url = systemKey.Value
	}
	return nil
}
