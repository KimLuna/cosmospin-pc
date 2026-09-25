package cosmo

import "context"

// Artist info endpoint (/artists/{id}): group metadata, the member roster, and
// official SNS links. The response also carries contract addresses and image
// URLs, which are not modeled here.

// Artist is a group's public card. Title is the display casing ("ARTMS"),
// while ID/Name are the API identifier ("artms").
// ComoTokenID is the artist's COMO id within the shared ERC-1155 COMO contract
// on Abstract (tripleS 1, artms 2, idntt 3). Gravity votes transfer that id, so
// it must come from here rather than be assumed: spending id 1 on an artms
// gravity would move the wrong artist's COMO.
type Artist struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	FandomName  string             `json:"fandomName"`
	ComoTokenID int64              `json:"comoTokenId"`
	Members     []ArtistMember     `json:"artistMembers"`
	SNSLink     map[string]SNSLink `json:"snsLink"`
}

// ArtistMember is one roster entry. Alias is the short codename ("S1", "id9");
// for artists without codenames it equals Name. Units is comma-separated and
// may be empty.
type ArtistMember struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Units    string `json:"units"`
	Order    int    `json:"order"`
	ColorHex string `json:"primaryColorHex"`
}

// SNSLink is one official social link, keyed by platform in Artist.SNSLink.
type SNSLink struct {
	Address string `json:"address"`
}

// Artist fetches a group's info card.
func (c *Client) Artist(ctx context.Context, artist string) (Artist, error) {
	var a Artist
	if err := c.getJSON(ctx, "/artists/"+artist, nil, &a); err != nil {
		return Artist{}, err
	}
	return a, nil
}
