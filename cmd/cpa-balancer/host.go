package main

import (
	"encoding/json"
	"errors"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// hostClient implements engine.Host on top of CPA host callbacks.
type hostClient struct{}

func (hostClient) call(method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	raw, err := callHost(method, body)
	if err != nil {
		return err
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if !env.OK {
		if env.Error != nil {
			return errors.New(env.Error.Message)
		}
		return errors.New(method + " failed")
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

func (h hostClient) AuthList() ([]pluginapi.HostAuthFileEntry, error) {
	var res struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	err := h.call(pluginabi.MethodHostAuthList, map[string]any{}, &res)
	return res.Files, err
}

func (h hostClient) AuthGet(authIndex string) ([]byte, error) {
	var res pluginapi.HostAuthGetResponse
	err := h.call(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex}, &res)
	return res.JSON, err
}

func (h hostClient) HTTPDo(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	var res pluginapi.HTTPResponse
	err := h.call(pluginabi.MethodHostHTTPDo, req, &res)
	return res, err
}

func (h hostClient) ModelExecute(req pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error) {
	var res pluginapi.HostModelExecutionResponse
	err := h.call(pluginabi.MethodHostModelExecute, req, &res)
	return res, err
}

func (h hostClient) Log(level, message string) {
	_ = h.call(pluginabi.MethodHostLog, map[string]any{"level": level, "message": message}, nil)
}
