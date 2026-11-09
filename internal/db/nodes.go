package db

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"router-go/internal/provider"
)

const (
	NodeTypeOpenAI    = "openai-compatible"
	NodeTypeAnthropic = "anthropic-compatible"
	NodeTypeBuiltin   = "builtin" // a registry provider (Cline, CodeBuddy, …): synthesised, never stored in providerNodes
)

type Node struct {
	ID, Type, Name, CreatedAt, UpdatedAt string
	Data                                 map[string]any // prefix, apiType, baseUrl, ...
}

func (n *Node) Prefix() string { return str(n.Data, "prefix") }

// Builtin returns the registry definition behind a built-in provider node.
func (n *Node) Builtin() (provider.Provider, bool) {
	if n.Type != NodeTypeBuiltin {
		return provider.Provider{}, false
	}
	return provider.ByID(n.ID)
}

func nodeFromRegistry(p provider.Provider) *Node {
	return &Node{ID: p.ID, Type: NodeTypeBuiltin, Name: p.Name,
		Data: map[string]any{"prefix": p.Alias, "apiType": "chat", "baseUrl": p.BaseURL, "builtin": true}}
}
func (n *Node) APIType() string { return str(n.Data, "apiType") }

// Format is the wire format the upstream speaks: "chat", "responses" or "messages" (Anthropic).
func (n *Node) Format() string {
	if n.Type == NodeTypeAnthropic {
		return "messages"
	}
	if n.APIType() == "responses" {
		return "responses"
	}
	return "chat"
}
func (n *Node) BaseURL() string { return str(n.Data, "baseUrl") }

// Flat is the XRouter-Next JSON shape (data fields merged with columns).
func (n *Node) Flat() map[string]any {
	m := map[string]any{}
	for k, v := range n.Data {
		m[k] = v
	}
	m["id"], m["type"], m["name"], m["createdAt"], m["updatedAt"] = n.ID, n.Type, n.Name, n.CreatedAt, n.UpdatedAt
	return m
}

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var typ, name sql.NullString
	var data string
	if err := sc.Scan(&n.ID, &typ, &name, &data, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, err
	}
	n.Type, n.Name, n.Data = typ.String, name.String, splitData(data)
	return &n, nil
}

const nodeCols = `id,type,name,data,createdAt,updatedAt`

func (s *Store) Nodes(typ string) ([]*Node, error) {
	q, args := `SELECT `+nodeCols+` FROM providerNodes`, []any{}
	if typ != "" {
		q += ` WHERE type=?`
		args = append(args, typ)
	}
	rows, err := s.DB.Query(q+` ORDER BY createdAt`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) Node(id string) (*Node, error) {
	n, err := scanNode(s.DB.QueryRow(`SELECT `+nodeCols+` FROM providerNodes WHERE id=?`, id))
	if err == sql.ErrNoRows {
		if p, ok := provider.ByID(id); ok {
			return nodeFromRegistry(p), nil
		}
		return nil, ErrNotFound
	}
	return n, err
}

// NodeByPrefix finds the compat node (openai or anthropic) that owns a model prefix.
func (s *Store) NodeByPrefix(prefix string) (*Node, error) {
	n, err := scanNode(s.DB.QueryRow(`SELECT `+nodeCols+` FROM providerNodes WHERE type IN (?,?) AND json_extract(data,'$.prefix')=? LIMIT 1`,
		NodeTypeOpenAI, NodeTypeAnthropic, prefix))
	if err == sql.ErrNoRows {
		if p, ok := provider.ByAlias(prefix); ok {
			return nodeFromRegistry(p), nil
		}
		return nil, ErrNotFound
	}
	return n, err
}

// CreateNode adds a compat node. typ is NodeTypeOpenAI (apiType chat|responses) or NodeTypeAnthropic (apiType ignored).
func (s *Store) CreateNode(typ, name, prefix, apiType, baseURL string) (*Node, error) {
	if ex, _ := s.NodeByPrefix(prefix); ex != nil {
		return nil, ErrConflict
	}
	t := now()
	n := &Node{ID: NodeTypeAnthropic + "-" + uuid.NewString(), Type: NodeTypeAnthropic, Name: name,
		Data: map[string]any{"prefix": prefix, "baseUrl": baseURL}, CreatedAt: t, UpdatedAt: t}
	if typ != NodeTypeAnthropic {
		n.ID, n.Type = NodeTypeOpenAI+"-"+apiType+"-"+uuid.NewString(), NodeTypeOpenAI
		n.Data["apiType"] = apiType
	}
	_, err := s.DB.Exec(`INSERT INTO providerNodes(`+nodeCols+`) VALUES(?,?,?,?,?,?)`, n.ID, n.Type, n.Name, joinData(n.Data), t, t)
	return n, err
}

// UpdateNode patches name/prefix/baseUrl/apiType and refreshes the denormalised copy on its connections.
func (s *Store) UpdateNode(id string, patch map[string]any) (*Node, error) {
	var out *Node
	err := s.withTx(func(tx *sql.Tx) error {
		n, err := scanNode(tx.QueryRow(`SELECT `+nodeCols+` FROM providerNodes WHERE id=?`, id))
		if err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if p, ok := patch["prefix"].(string); ok && p != n.Prefix() {
			var c int
			_ = tx.QueryRow(`SELECT COUNT(*) FROM providerNodes WHERE id<>? AND type IN (?,?) AND json_extract(data,'$.prefix')=?`, id, NodeTypeOpenAI, NodeTypeAnthropic, p).Scan(&c)
			if _, builtin := provider.ByAlias(p); c > 0 || builtin {
				return ErrConflict
			}
		}
		oldPrefix := n.Prefix()
		for _, k := range []string{"prefix", "baseUrl"} {
			if v, ok := patch[k].(string); ok {
				n.Data[k] = v
			}
		}
		if n.Prefix() != oldPrefix && oldPrefix != "" {
			if err := renamePrefix(tx, oldPrefix, n.Prefix()); err != nil {
				return err
			}
		}
		if v, ok := patch["name"].(string); ok {
			n.Name = v
		}
		n.UpdatedAt = now()
		if _, err := tx.Exec(`UPDATE providerNodes SET name=?, data=?, updatedAt=? WHERE id=?`, n.Name, joinData(n.Data), n.UpdatedAt, id); err != nil {
			return err
		}
		// connections carry a copy of the node fields in providerSpecificData
		_, err = tx.Exec(`UPDATE providerConnections SET data=json_set(data,
			'$.providerSpecificData.prefix', ?, '$.providerSpecificData.baseUrl', ?, '$.providerSpecificData.nodeName', ?)
			WHERE provider=?`, n.Prefix(), n.BaseURL(), n.Name, id)
		out = n
		return err
	})
	return out, err
}

func (s *Store) DeleteNode(id string) error {
	return s.withTx(func(tx *sql.Tx) error {
		var prefix sql.NullString
		_ = tx.QueryRow(`SELECT json_extract(data,'$.prefix') FROM providerNodes WHERE id=?`, id).Scan(&prefix)
		r, err := tx.Exec(`DELETE FROM providerNodes WHERE id=?`, id)
		if err != nil {
			return err
		}
		if c, _ := r.RowsAffected(); c == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM providerConnections WHERE provider=?`, id); err != nil {
			return err
		}
		if prefix.String == "" {
			return nil
		}
		// custom models are keyed "<prefix>|<id>|<type>"
		_, err = tx.Exec(`DELETE FROM kv WHERE scope='customModels' AND substr(key,1,?)=?`, len(prefix.String)+1, prefix.String+"|")
		return err
	})
}

// CreatedUnix is the creation time as a Unix timestamp (0 if unparsable).
func (n *Node) CreatedUnix() int64 {
	t, err := time.Parse(time.RFC3339Nano, n.CreatedAt)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// renamePrefix keeps everything that refers to "<old>/model" working after a node's prefix changes:
// custom models, model aliases, combo members and API-key allow lists.
func renamePrefix(tx *sql.Tx, oldP, newP string) error {
	op := oldP + "/"
	// custom models are keyed "<prefix>|<id>|<type>" and repeat the prefix as providerAlias
	if _, err := tx.Exec(`UPDATE kv SET key=? || substr(key, ?), value=json_set(value,'$.providerAlias',?)
		WHERE scope='customModels' AND substr(key,1,?)=?`, newP+"|", len(oldP)+2, newP, len(oldP)+1, oldP+"|"); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE kv SET value=json_quote(? || substr(trim(value,'"'), ?))
		WHERE scope='modelAliases' AND substr(trim(value,'"'),1,?)=?`, newP+"/", len(op)+1, len(op), op); err != nil {
		return err
	}
	for _, t := range []struct{ table, col string }{{"combos", "models"}, {"apiKeys", "accessAllow"}} {
		if err := renameInJSONList(tx, t.table, t.col, op, newP+"/"); err != nil {
			return err
		}
	}
	return nil
}

// renameInJSONList rewrites entries starting with from (case-insensitively for the prefix) in a JSON string-array column.
func renameInJSONList(tx *sql.Tx, table, col, from, to string) error {
	rows, err := tx.Query(`SELECT rowid, `+col+` FROM `+table+` WHERE `+col+` LIKE '%' || ? || '%'`, from[:len(from)-1])
	if err != nil {
		return err
	}
	type upd struct {
		rowid int64
		val   string
	}
	var ups []upd
	for rows.Next() {
		var id int64
		var raw sql.NullString
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var list []string
		if json.Unmarshal([]byte(raw.String), &list) != nil {
			continue
		}
		changed := false
		for i, e := range list {
			if len(e) >= len(from) && strings.EqualFold(e[:len(from)], from) {
				list[i], changed = to+e[len(from):], true
			}
		}
		if changed {
			b, _ := json.Marshal(list)
			ups = append(ups, upd{id, string(b)})
		}
	}
	rows.Close()
	for _, u := range ups {
		if _, err := tx.Exec(`UPDATE `+table+` SET `+col+`=? WHERE rowid=?`, u.val, u.rowid); err != nil {
			return err
		}
	}
	return nil
}
