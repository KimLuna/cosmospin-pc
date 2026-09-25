// Package members is the static roster for the Cosmo groups.
//
// Members are keyed by their official group number and carry the APIID the
// Cosmo API expects (artistMemberId, == the member's id under
// /bff/v3/artists/{group}). This is a static snapshot so nothing needs an extra
// API call at runtime; regenerate from the live roster when membership changes.
package members

import (
	"sort"
	"strconv"
	"strings"
)

// Member is one roster entry.
type Member struct {
	Number int    // official group number (also the API "order"/alias number)
	APIID  int    // artistMemberId the API expects
	Name   string // display name
}

// roster maps canonical group -> official number -> (apiID, name).
var roster = map[string]map[int]Member{
	"tripleS": {
		1:  {1, 1, "SeoYeon"},
		2:  {2, 2, "HyeRin"},
		3:  {3, 3, "JiWoo"},
		4:  {4, 4, "ChaeYeon"},
		5:  {5, 5, "YooYeon"},
		6:  {6, 6, "SooMin"},
		7:  {7, 7, "NaKyoung"},
		8:  {8, 8, "YuBin"},
		9:  {9, 9, "Kaede"},
		10: {10, 10, "DaHyun"},
		11: {11, 11, "Kotone"},
		12: {12, 12, "YeonJi"},
		13: {13, 13, "Nien"},
		14: {14, 14, "SoHyun"},
		15: {15, 19, "Xinyu"},
		16: {16, 21, "Mayu"},
		17: {17, 22, "Lynn"},
		18: {18, 23, "JooBin"},
		19: {19, 24, "HaYeon"},
		20: {20, 25, "ShiOn"},
		21: {21, 26, "ChaeWon"},
		22: {22, 27, "Sullin"},
		23: {23, 28, "SeoAh"},
		24: {24, 29, "JiYeon"},
	},
	"artms": {
		1: {1, 15, "HeeJin"},
		2: {2, 20, "HaSeul"},
		3: {3, 16, "KimLip"},
		4: {4, 17, "JinSoul"},
		5: {5, 18, "Choerry"},
	},
	"idntt": {
		1:  {1, 30, "DoHun"},
		2:  {2, 31, "HeeJu"},
		4:  {4, 33, "TaeIn"},
		5:  {5, 34, "JaeYoung"},
		6:  {6, 35, "JuHo"},
		7:  {7, 36, "JiWoon"},
		8:  {8, 37, "HwanHee"},
		9:  {9, 38, "CheongMyeong"},
		10: {10, 39, "Towa"},
		11: {11, 40, "KyuHyuk"},
		12: {12, 41, "NuRi"},
		13: {13, 42, "SeongJun"},
		14: {14, 43, "YeJoon"},
		15: {15, 44, "GyeongBeen"},
		16: {16, 45, "EunSoo"},
		17: {17, 46, "GiWoong"},
		18: {18, 47, "JooHeon"},
		19: {19, 48, "GyungHo"},
		20: {20, 49, "EunChan"},
		21: {21, 50, "EunSung"},
	},
}

// Groups lists the canonical group ids in a stable order.
var Groups = []string{"tripleS", "artms", "idntt"}

// NormalizeGroup resolves a group name case-insensitively to its canonical
// form, returning ("", false) if unknown.
func NormalizeGroup(group string) (string, bool) {
	g := strings.ToLower(strings.TrimSpace(group))
	for canonical := range roster {
		if strings.ToLower(canonical) == g {
			return canonical, true
		}
	}
	return "", false
}

// ResolveMember resolves a member by official number or name (both
// case-insensitive) within a group.
func ResolveMember(group, input string) (Member, bool) {
	canonical, ok := NormalizeGroup(group)
	if !ok {
		return Member{}, false
	}
	r := roster[canonical]
	text := strings.TrimSpace(input)

	if num, err := strconv.Atoi(text); err == nil {
		m, ok := r[num]
		return m, ok
	}
	for _, m := range r {
		if strings.EqualFold(m.Name, text) {
			return m, true
		}
	}
	return Member{}, false
}

// AllMembers returns a group's members in official-number order.
func AllMembers(group string) []Member {
	canonical, ok := NormalizeGroup(group)
	if !ok {
		return nil
	}
	r := roster[canonical]
	nums := make([]int, 0, len(r))
	for n := range r {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]Member, 0, len(nums))
	for _, n := range nums {
		out = append(out, r[n])
	}
	return out
}
