/*
Licensed to the Apache Software Foundation (ASF) under one or more
contributor license agreements.  See the NOTICE file distributed with
this work for additional information regarding copyright ownership.
The ASF licenses this file to You under the Apache License, Version 2.0
(the "License"); you may not use this file except in compliance with
the License.  You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tasks

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/apache/incubator-devlake/errors"
	"github.com/apache/incubator-devlake/plugins/core"
	"github.com/apache/incubator-devlake/plugins/core/dal"

	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/apache/incubator-devlake/plugins/helper"
)

const RAW_EPIC_TABLE = "jira_api_epics"

var _ core.SubTaskEntryPoint = CollectEpics

var CollectEpicsMeta = core.SubTaskMeta{
	Name:             "collectEpics",
	EntryPoint:       CollectEpics,
	EnabledByDefault: true,
	Description:      "collect Jira epics from all boards",
	DomainTypes:      []string{core.DOMAIN_TYPE_TICKET, core.DOMAIN_TYPE_CROSS},
}

func CollectEpics(taskCtx core.SubTaskContext) errors.Error {
	db := taskCtx.GetDal()
	data := taskCtx.GetData().(*JiraTaskData)
	epicIterator, err := GetEpicKeysIterator(db, data, 100)
	if err != nil {
		return err
	}
	since := data.Since
	jql := "ORDER BY created ASC"
	if since != nil {
		// prepend a time range criteria if `since` was specified, either by user or from database
		jql = fmt.Sprintf("updated >= '%s' %s", since.Format("2006/01/02 15:04"), jql)
	}
	collector, err := helper.NewApiCollector(helper.ApiCollectorArgs{
		RawDataSubTaskArgs: helper.RawDataSubTaskArgs{
			Ctx: taskCtx,
			Params: JiraApiParams{
				ConnectionId: data.Options.ConnectionId,
				BoardId:      data.Options.BoardId,
			},
			Table: RAW_EPIC_TABLE,
		},
		ApiClient:   data.ApiClient,
		PageSize:    100,
		Incremental: false,
		UrlTemplate: "api/2/search",
		Query: func(reqData *helper.RequestData) (url.Values, errors.Error) {
			query := url.Values{}
			epicKeys := []string{}
			for _, e := range reqData.Input.([]interface{}) {
				epicKeys = append(epicKeys, *e.(*string))
			}
			localJQL := fmt.Sprintf("issue in (%s) %s", strings.Join(epicKeys, ","), jql)
			query.Set("jql", localJQL)
			query.Set("startAt", fmt.Sprintf("%v", reqData.Pager.Skip))
			query.Set("maxResults", fmt.Sprintf("%v", reqData.Pager.Size))
			query.Set("expand", "changelog")
			return query, nil
		},
		Input:         epicIterator,
		GetTotalPages: GetTotalPagesFromResponse,
		Concurrency:   10,
		ResponseParser: func(res *http.Response) ([]json.RawMessage, errors.Error) {
			var data struct {
				Issues []json.RawMessage `json:"issues"`
			}
			blob, err := io.ReadAll(res.Body)
			if err != nil {
				return nil, errors.Convert(err)
			}
			err = json.Unmarshal(blob, &data)
			if err != nil {
				return nil, errors.Convert(err)
			}
			return data.Issues, nil
		},
	})
	if err != nil {
		return err
	}
	return collector.Execute()
}

func GetEpicKeysIterator(db dal.Dal, data *JiraTaskData, batchSize int) (helper.Iterator, errors.Error) {
	cursor, err := db.Cursor(
		dal.Select("DISTINCT epic_key"),
		dal.From("_tool_jira_issues i"),
		dal.Join(`
			LEFT JOIN _tool_jira_board_issues bi ON (
			i.connection_id = bi.connection_id
			AND 
			i.issue_id = bi.issue_id
		)`),
		dal.Where(`
			i.connection_id = ?
			AND 
			bi.board_id = ?
			AND
			i.epic_key != ''
		`, data.Options.ConnectionId, data.Options.BoardId,
		),
	)
	if err != nil {
		return nil, errors.Default.Wrap(err, "unable to query for external epics")
	}
	iter, err := helper.NewBatchedDalCursorIterator(db, cursor, reflect.TypeOf(""), batchSize)
	if err != nil {
		return nil, err
	}
	return iter, nil
}
