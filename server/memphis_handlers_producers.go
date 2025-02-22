// Copyright 2022-2023 The Memphis.dev Authors
// Licensed under the Memphis Business Source License 1.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// Changed License: [Apache License, Version 2.0 (https://www.apache.org/licenses/LICENSE-2.0), as published by the Apache Foundation.
//
// https://github.com/memphisdev/memphis/blob/master/LICENSE
//
// Additional Use Grant: You may make use of the Licensed Work (i) only as part of your own product or service, provided it is not a message broker or a message queue product or service; and (ii) provided that you do not use, provide, distribute, or make available the Licensed Work as a Service.
// A "Service" is a commercial offering, product, hosted, or managed service, that allows third parties (other than your own employees and contractors acting on your behalf) to access and/or use the Licensed Work or a substantial set of the features or functionality of the Licensed Work to third parties as a software-as-a-service, platform-as-a-service, infrastructure-as-a-service or other similar services that compete with Licensor products or services.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"memphis/analytics"
	"memphis/db"
	"memphis/models"
	"memphis/utils"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/utils/strings/slices"
)

type ProducersHandler struct{ S *Server }

const (
	producerObjectName = "Producer"
)

func validateProducerName(name string) error {
	return validateName(name, producerObjectName)
}

func validateProducerType(producerType string) error {
	if producerType != "application" && producerType != "connector" {
		return errors.New("producer type has to be one of the following application/connector")
	}
	return nil
}

func (s *Server) createProducerDirectCommon(c *client, pName, pType, pConnectionId string, pStationName StationName) (bool, bool, error) {
	name := strings.ToLower(pName)
	err := validateProducerName(name)
	if err != nil {
		serv.Warnf("createProducerDirectCommon at validateProducerName: Producer %v at station %v: %v", pName, pStationName.external, err.Error())
		return false, false, err
	}

	producerType := strings.ToLower(pType)
	err = validateProducerType(producerType)
	if err != nil {
		serv.Warnf("createProducerDirectCommon at validateProducerType: Producer %v at station %v: %v", pName, pStationName.external, err.Error())
		return false, false, err
	}

	exist, connection, err := db.GetConnectionByID(pConnectionId)
	if err != nil {
		serv.Errorf("createProducerDirectCommon at GetConnectionByIDa: Producer %v at station %v: %v", pName, pStationName.external, err.Error())
		return false, false, err
	}
	if !exist {
		errMsg := fmt.Sprintf("Connection ID %v was not found", pConnectionId)
		serv.Warnf("createProducerDirectCommon: Producer %v at station %v: %v", pName, pStationName.external, errMsg)
		return false, false, fmt.Errorf("memphis: %v", errMsg)
	}

	exist, user, err := db.GetUserByUserId(connection.CreatedBy)
	if err != nil {
		serv.Errorf("createProducerDirectCommon at GetUserByUserId: creating default station error - producer %v at station %v: %v", pName, pStationName.external, err.Error())
		return false, false, err
	}
	if !exist {
		serv.Warnf("createProducerDirectCommon at GetUserByUserId: user %v is not exists", user.Username)
		return false, false, err
	}

	exist, station, err := db.GetStationByName(pStationName.Ext(), user.TenantName)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]createProducerDirectCommon at GetStationByName: Producer %v at station %v: %v", user.TenantName, user.Username, pName, pStationName.external, err.Error())
		return false, false, err
	}
	if !exist {
		var created bool
		station, created, err = CreateDefaultStation(user.TenantName, s, pStationName, connection.CreatedBy, user.Username)
		if err != nil {
			serv.Errorf("[tenant: %v][user: %v]createProducerDirectCommon at CreateDefaultStation: creating default station error - producer %v at station %v: %v", user.TenantName, user.Username, pName, pStationName.external, err.Error())
			return false, false, err
		}
		if created {
			message := "Station " + pStationName.Ext() + " has been created by user " + user.Username
			serv.Noticef("[tenant: %v][user: %v]: %v", user.TenantName, user.Username, message)
			var auditLogs []interface{}
			newAuditLog := models.AuditLog{
				StationName:       pStationName.Ext(),
				Message:           message,
				CreatedBy:         connection.CreatedBy,
				CreatedByUsername: connection.CreatedByUsername,
				CreatedAt:         time.Now(),
				TenantName:        user.TenantName,
			}
			auditLogs = append(auditLogs, newAuditLog)
			err = CreateAuditLogs(auditLogs)
			if err != nil {
				serv.Errorf("[tenant: %v][user: %v]createProducerDirectCommon: Producer %v at station %v: %v", user.TenantName, user.Username, pName, pStationName.external, err.Error())
			}

			shouldSendAnalytics, _ := shouldSendAnalytics()
			if shouldSendAnalytics {
				analyticsParams := map[string]interface{}{"station-name": pStationName.Ext(), "storage-type": "disk"}
				analytics.SendEvent(user.TenantName, user.Username, analyticsParams, "user-create-station-sdk")
			}
		}
	}

	exist, _, err = db.GetActiveProducerByStationID(name, station.ID)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]createProducerDirectCommon at GetActiveProducerByStationID: Producer %v at station %v: %v", user.TenantName, user.Username, pName, pStationName.external, err.Error())
		return false, false, err
	}
	if exist {
		errMsg := fmt.Sprintf("Producer name ( %v ) has to be unique per station ( %v )", pName, pStationName.external)
		serv.Warnf("[tenant: %v][user: %v]createProducerDirectCommon: %v", user.TenantName, user.Username, errMsg)
		return false, false, fmt.Errorf("memphis: %v", errMsg)
	}
	newProducer, rowsUpdated, err := db.InsertNewProducer(name, station.ID, producerType, pConnectionId, connection.CreatedBy, user.Username, station.TenantName)
	if err != nil {
		serv.Warnf("[tenant: %v][user: %v]createProducerDirectCommon at InsertNewProducer: %v", user.TenantName, user.Username, err.Error())
		return false, false, err
	}
	if rowsUpdated == 1 {
		message := "Producer " + name + " has been created by user " + user.Username
		serv.Noticef("[tenant: %v][user: %v]: %v", user.TenantName, user.Username, message)
		var auditLogs []interface{}
		newAuditLog := models.AuditLog{
			StationName:       pStationName.Ext(),
			Message:           message,
			CreatedBy:         connection.CreatedBy,
			CreatedByUsername: connection.CreatedByUsername,
			CreatedAt:         time.Now(),
			TenantName:        user.TenantName,
		}
		auditLogs = append(auditLogs, newAuditLog)
		err = CreateAuditLogs(auditLogs)
		if err != nil {
			serv.Errorf("[tenant: %v][user: %v]createProducerDirectCommon at CreateAuditLogs: Producer %v at station %v: %v", user.TenantName, user.Username, pName, pStationName.external, err.Error())
		}

		shouldSendAnalytics, _ := shouldSendAnalytics()
		if shouldSendAnalytics {
			analyticsParams := map[string]interface{}{"producer-name": newProducer.Name}
			analytics.SendEvent(user.TenantName, connection.CreatedByUsername, analyticsParams, "user-create-producer-sdk")
			if strings.HasPrefix(newProducer.Name, "rest_gateway") {
				analyticsParams = map[string]interface{}{}
				analytics.SendEvent(user.TenantName, connection.CreatedByUsername, analyticsParams, "user-send-messages-via-rest-gw")
			}
		}
	}
	shouldSendNotifications, err := IsSlackEnabled(user.TenantName)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]createProducerDirectCommon at IsSlackEnabled: Producer %v at station %v: %v", user.TenantName, user.Username, pName, pStationName.external, err.Error())
	}

	return shouldSendNotifications, station.DlsConfigurationSchemaverse, nil
}

func (s *Server) createProducerDirectV0(c *client, reply string, cpr createProducerRequestV0) {
	sn, err := StationNameFromStr(cpr.StationName)
	if err != nil {
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
		return
	}
	_, _, err = s.createProducerDirectCommon(c, cpr.Name,
		cpr.ProducerType, cpr.ConnectionId, sn)
	respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
}

func (s *Server) createProducerDirect(c *client, reply string, msg []byte) {
	var cpr createProducerRequestV1
	var resp createProducerResponse

	tenantName, message, err := s.getTenantNameAndMessage(msg)
	if err != nil {
		s.Errorf("createProducerDirect: %v", err.Error())
		return
	}

	if err := json.Unmarshal([]byte(message), &cpr); err != nil || cpr.RequestVersion < 1 {
		var cprV0 createProducerRequestV0
		if err := json.Unmarshal([]byte(message), &cprV0); err != nil {
			s.Errorf("[tenant: %v]createProducerDirect: %v", tenantName, err.Error())
			respondWithRespErr(s.MemphisGlobalAccountString(), s, reply, err, &resp)
			return
		}
		s.createProducerDirectV0(c, reply, cprV0)
		return
	}
	cpr.TenantName = tenantName
	sn, err := StationNameFromStr(cpr.StationName)
	if err != nil {
		s.Warnf("[tenant: %v][user: %v]createProducerDirect at StationNameFromStr: Producer %v at station %v: %v", cpr.TenantName, cpr.Username, cpr.Name, cpr.StationName, err.Error())
		respondWithRespErr(s.MemphisGlobalAccountString(), s, reply, err, &resp)
		return
	}

	clusterSendNotification, schemaVerseToDls, err := s.createProducerDirectCommon(c, cpr.Name, cpr.ProducerType, cpr.ConnectionId, sn)
	if err != nil {
		respondWithRespErr(s.MemphisGlobalAccountString(), s, reply, err, &resp)
		return
	}

	resp.SchemaVerseToDls = schemaVerseToDls
	resp.ClusterSendNotification = clusterSendNotification
	schemaUpdate, err := getSchemaUpdateInitFromStation(sn, cpr.TenantName)
	if err == ErrNoSchema {
		respondWithResp(s.MemphisGlobalAccountString(), s, reply, &resp)
		return
	}
	if err != nil {
		s.Errorf("[tenant: %v][user: %v]createProducerDirect at getSchemaUpdateInitFromStation: Producer %v at station %v: %v", cpr.TenantName, cpr.Username, cpr.Name, cpr.StationName, err.Error())
		respondWithRespErr(s.MemphisGlobalAccountString(), s, reply, err, &resp)
		return
	}

	resp.SchemaUpdate = *schemaUpdate
	respondWithResp(s.MemphisGlobalAccountString(), s, reply, &resp)
}

func (ph ProducersHandler) GetAllProducers(c *gin.Context) {
	producers, err := db.GetAllProducers()
	if err != nil {
		serv.Errorf("GetAllProducers at db.GetAllProducers: %v", err.Error())
		c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
		return
	}

	if len(producers) == 0 {
		c.IndentedJSON(200, []string{})
	} else {
		c.IndentedJSON(200, producers)
	}
}

func (ph ProducersHandler) GetProducersByStation(station models.Station) ([]models.ExtendedProducer, []models.ExtendedProducer, []models.ExtendedProducer, error) { // for socket io endpoint
	producers, err := db.GetAllProducersByStationID(station.ID)
	if err != nil {
		return producers, producers, producers, err
	}

	var connectedProducers []models.ExtendedProducer
	var disconnectedProducers []models.ExtendedProducer
	var deletedProducers []models.ExtendedProducer
	producersNames := []string{}

	for _, producer := range producers {
		if slices.Contains(producersNames, producer.Name) {
			continue
		}

		producerRes := models.ExtendedProducer{
			ID:                producer.ID,
			Name:              producer.Name,
			CreatedByUsername: producer.CreatedByUsername,
			StationName:       producer.StationName,
			CreatedAt:         producer.CreatedAt,
			IsActive:          producer.IsActive,
			IsDeleted:         producer.IsDeleted,
			ClientAddress:     producer.ClientAddress,
		}

		producersNames = append(producersNames, producer.Name)
		if producer.IsActive {
			connectedProducers = append(connectedProducers, producerRes)
		} else if !producer.IsDeleted && !producer.IsActive {
			disconnectedProducers = append(disconnectedProducers, producerRes)
		} else if producer.IsDeleted {
			deletedProducers = append(deletedProducers, producerRes)
		}
	}

	if len(connectedProducers) == 0 {
		connectedProducers = []models.ExtendedProducer{}
	}

	if len(disconnectedProducers) == 0 {
		disconnectedProducers = []models.ExtendedProducer{}
	}

	if len(deletedProducers) == 0 {
		deletedProducers = []models.ExtendedProducer{}
	}

	sort.Slice(connectedProducers, func(i, j int) bool {
		return connectedProducers[j].CreatedAt.Before(connectedProducers[i].CreatedAt)
	})
	sort.Slice(disconnectedProducers, func(i, j int) bool {
		return disconnectedProducers[j].CreatedAt.Before(disconnectedProducers[i].CreatedAt)
	})
	sort.Slice(deletedProducers, func(i, j int) bool {
		return deletedProducers[j].CreatedAt.Before(deletedProducers[i].CreatedAt)
	})
	return connectedProducers, disconnectedProducers, deletedProducers, nil
}

func (ph ProducersHandler) GetAllProducersByStation(c *gin.Context) { // for the REST endpoint (using cli)
	var body models.GetAllProducersByStationSchema
	ok := utils.Validate(c, &body, false, nil)
	if !ok {
		return
	}

	user, err := getUserDetailsFromMiddleware(c)
	if err != nil {
		serv.Errorf("GetAllProducersByStation: %v", err.Error())
		c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
		return
	}

	stationName, _ := StationNameFromStr(body.StationName)
	exist, station, err := db.GetStationByName(stationName.Ext(), user.TenantName)
	if err != nil {
		c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
		return
	}
	if !exist {
		serv.Warnf("GetAllProducersByStation: Station %v does not exist", body.StationName)
		c.AbortWithStatusJSON(SHOWABLE_ERROR_STATUS_CODE, gin.H{"message": "Station does not exist"})
		return
	}

	producers, err := db.GetNotDeletedProducersByStationID(station.ID)
	if err != nil {
		serv.Errorf("[tenant: %v]GetAllProducersByStation at GetNotDeletedProducersByStationID: Station %v :%v ", station.TenantName, body.StationName, err.Error())
		c.AbortWithStatusJSON(500, gin.H{"message": "Server error"})
		return
	}

	if len(producers) == 0 {
		c.IndentedJSON(200, []string{})
	} else {
		c.IndentedJSON(200, producers)
	}
}

func (s *Server) destroyProducerDirect(c *client, reply string, msg []byte) {
	var dpr destroyProducerRequest
	tenantName, destoryMessage, err := s.getTenantNameAndMessage(msg)
	if err != nil {
		s.Errorf("destroyProducerDirect: %v", err.Error())
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
		return
	}
	if err := json.Unmarshal([]byte(destoryMessage), &dpr); err != nil {
		s.Errorf("destroyProducerDirect: %v", err.Error())
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
		return
	}

	dpr.TenantName = tenantName
	stationName, err := StationNameFromStr(dpr.StationName)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]destroyProducerDirect at StationNameFromStr: Producer %v at station %v: %v", dpr.TenantName, dpr.Username, dpr.ProducerName, dpr.StationName, err.Error())
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
		return
	}
	name := strings.ToLower(dpr.ProducerName)
	_, station, err := db.GetStationByName(stationName.Ext(), dpr.TenantName)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]destroyProducerDirect at GetStationByName: Producer %v at station %v: %v", dpr.TenantName, dpr.Username, dpr.ProducerName, dpr.StationName, err.Error())
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
		return
	}

	exist, _, err := db.DeleteProducerByNameAndStationID(name, station.ID)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]destroyProducerDirect at DeleteProducerByNameAndStationID: Producer %v at station %v: %v", dpr.TenantName, dpr.Username, name, dpr.StationName, err.Error())
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, err)
		return
	}
	if !exist {
		errMsg := fmt.Sprintf("Producer %v at station %v does not exist", name, dpr.StationName)
		serv.Warnf("[tenant: %v][user: %v]destroyProducerDirect: %v", dpr.TenantName, dpr.Username, errMsg)
		respondWithErr(s.MemphisGlobalAccountString(), s, reply, errors.New(errMsg))
		return
	}

	username := c.memphisInfo.username
	if username == "" {
		username = dpr.Username
	}
	_, user, err := db.GetUserByUsername(username, dpr.TenantName)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]destroyProducerDirect at GetUserByUsername: Producer %v at station %v: %v", dpr.TenantName, dpr.Username, name, dpr.StationName, err.Error())
	}
	message := "Producer " + name + " has been deleted by user " + username
	serv.Noticef("[tenant: %v][user: %v]: %v", tenantName, username, message)
	var auditLogs []interface{}
	newAuditLog := models.AuditLog{
		StationName:       stationName.Ext(),
		Message:           message,
		CreatedBy:         user.ID,
		CreatedByUsername: user.Username,
		CreatedAt:         time.Now(),
		TenantName:        user.TenantName,
	}
	auditLogs = append(auditLogs, newAuditLog)
	err = CreateAuditLogs(auditLogs)
	if err != nil {
		serv.Errorf("[tenant: %v][user: %v]destroyProducerDirect at CreateAuditLogs: Producer %v at station %v: %v", dpr.TenantName, dpr.Username, name, dpr.StationName, err.Error())
	}

	shouldSendAnalytics, _ := shouldSendAnalytics()
	if shouldSendAnalytics {
		analyticsParams := make(map[string]interface{})
		analytics.SendEvent(user.TenantName, username, analyticsParams, "user-remove-producer-sdk")
	}

	respondWithErr(s.MemphisGlobalAccountString(), s, reply, nil)
}
