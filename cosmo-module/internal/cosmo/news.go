package cosmo

import (
	"context"
	"net/url"
	"strconv"
)

// News-tab endpoints: announcements (/notices), the artist schedule
// (/artist-schedules) and the notification feed (/notification-center). The
// first two have a list call (needs artistId) and a by-id detail call (no
// artistId); the notification feed is list-only. The home-screen /news feed
// (banners/sections) is a separate widget and is not modeled here.

// newsPageSize takes the whole notice/schedule list in one call. Neither
// endpoint caps take — verified against the full lists (168 notices, 53
// schedules) — so this is a ceiling rather than a page size, and neither call
// pages. Note the Talk endpoints do cap take, silently, at 30; do not assume a
// large take works elsewhere without checking.
const newsPageSize = 1000

// notificationPageSize takes the whole notification feed in one call: the server
// only serves the last 14 days, which bounds the list (~110 entries for a busy
// group), so there is nothing to page through.
const notificationPageSize = 1000

// Notice is one announcement list entry. Category is "Notice" or "Content".
type Notice struct {
	ID       int    `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	ActiveAt string `json:"activeAt"`
}

// NoticeDetail is a full announcement. Content is plain text with newlines.
type NoticeDetail struct {
	ID           int      `json:"id"`
	Category     string   `json:"category"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	ImageURLList []string `json:"imageUrlList"`
	ActiveAt     string   `json:"activeAt"`
}

// Schedule is one schedule list entry. StartAt/EndAt are ISO-8601 with a KST
// (+09:00) offset.
type Schedule struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	StartAt string `json:"startAt"`
	EndAt   string `json:"endAt"`
}

// ScheduleDetail is a full schedule with place and participating members.
type ScheduleDetail struct {
	ID      int              `json:"id"`
	Title   string           `json:"title"`
	Content string           `json:"content"`
	StartAt string           `json:"startAt"`
	EndAt   string           `json:"endAt"`
	Place   string           `json:"place"`
	Members []ScheduleMember `json:"members"`
}

// ScheduleMember is one member attached to a schedule.
type ScheduleMember struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Notification is one entry of the notification feed: a live start, a replay
// becoming available, a new story, a new announcement, a shop drop. Content is
// the whole notification (there is no by-id detail call), and SentAt is
// RFC-3339 in UTC, unlike Schedule's KST offsets. Category is open-ended -
// "Room", "Etc" and "Shop" are what the API serves today, so render it verbatim
// rather than switching on it.
//
// The wire also carries "url" (a cosmo:// deep link into the app, e.g.
// cosmo://tripleS/notice?id=262) and "isRead"; neither is modeled because
// nothing renders them yet.
type Notification struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	SentAt   string `json:"sentAt"`
}

type noticesResponse struct {
	Result []Notice `json:"result"`
}

type noticeDetailResponse struct {
	Result NoticeDetail `json:"result"`
}

type schedulesResponse struct {
	Items []Schedule `json:"items"`
	Total int        `json:"total"`
}

type notificationsResponse struct {
	Notifications []Notification `json:"notifications"`
}

// Notices fetches the announcement list for a group, newest first (API order).
func (c *Client) Notices(ctx context.Context, group string) ([]Notice, error) {
	params := url.Values{
		"skip":     {"0"},
		"take":     {strconv.Itoa(newsPageSize)},
		"artistId": {group},
	}
	var resp noticesResponse
	if err := c.getJSON(ctx, "/notices", params, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// NoticeDetail fetches a single announcement by id.
func (c *Client) NoticeDetail(ctx context.Context, id int) (NoticeDetail, error) {
	var resp noticeDetailResponse
	if err := c.getJSON(ctx, "/notices/"+strconv.Itoa(id), nil, &resp); err != nil {
		return NoticeDetail{}, err
	}
	return resp.Result, nil
}

// Schedules fetches upcoming schedules for a group.
func (c *Client) Schedules(ctx context.Context, group string) ([]Schedule, error) {
	params := url.Values{
		"filter":   {"upcoming"},
		"take":     {strconv.Itoa(newsPageSize)},
		"skip":     {"0"},
		"artistId": {group},
	}
	var resp schedulesResponse
	if err := c.getJSON(ctx, "/artist-schedules", params, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ScheduleDetail fetches a single schedule by id.
func (c *Client) ScheduleDetail(ctx context.Context, id int) (ScheduleDetail, error) {
	var detail ScheduleDetail
	if err := c.getJSON(ctx, "/artist-schedules/"+strconv.Itoa(id), nil, &detail); err != nil {
		return ScheduleDetail{}, err
	}
	return detail, nil
}

// Notifications fetches the user's notification feed for a group, newest first
// (API order). artistId is required: the endpoint 400s without one, and an
// unknown group yields an empty feed rather than an error.
func (c *Client) Notifications(ctx context.Context, group string) ([]Notification, error) {
	params := url.Values{
		"skip":     {"0"},
		"take":     {strconv.Itoa(notificationPageSize)},
		"artistId": {group},
	}
	var resp notificationsResponse
	if err := c.getJSON(ctx, "/notification-center", params, &resp); err != nil {
		return nil, err
	}
	return resp.Notifications, nil
}
