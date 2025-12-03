//go:build linux

package network

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
)

//go:generate mockgen -package=mock -destination=mock/network_interfaces.go  -source=network_interfaces.go NetworkOperator,NFTablesOperator

// NetworkOperator defines interface for network operations
type NetworkOperator interface {
	LinkByName(name string) (netlink.Link, error)
	LinkAdd(link netlink.Link) error
	LinkDel(link netlink.Link) error
	LinkSetUp(link netlink.Link) error
	LinkSetMaster(link netlink.Link, master netlink.Link) error
	LinkList() ([]netlink.Link, error)
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
	AddrAdd(link netlink.Link, addr *netlink.Addr) error
	ParseAddr(s string) (*netlink.Addr, error)
	RouteList(link netlink.Link, family int) ([]netlink.Route, error)
	LinkByIndex(index int) (netlink.Link, error)
}

// NFTablesOperator defines interface for nftables operations
// NFTablesOperator interface additions
type NFTablesOperator interface {
	AddTable(table *nftables.Table) error
	AddChain(chain *nftables.Chain) error
	AddRule(rule *nftables.Rule) error
	DelTable(table *nftables.Table) error
	DelChain(chain *nftables.Chain) error
	GetChains(table *nftables.Table) ([]*nftables.Chain, error)
	GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error)
	Flush() error
	Close()
	FlushChain(chain *nftables.Chain) error
}

// DefaultNetworkOperator implements NetworkOperator using netlink
type DefaultNetworkOperator struct{}

func NewDefaultNetworkOperator() NetworkOperator {
	return &DefaultNetworkOperator{}
}

func (o *DefaultNetworkOperator) LinkByName(name string) (netlink.Link, error) {
	return netlink.LinkByName(name)
}

func (o *DefaultNetworkOperator) LinkAdd(link netlink.Link) error {
	return netlink.LinkAdd(link)
}

func (o *DefaultNetworkOperator) LinkDel(link netlink.Link) error {
	return netlink.LinkDel(link)
}

func (o *DefaultNetworkOperator) LinkSetUp(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

func (o *DefaultNetworkOperator) LinkSetMaster(link netlink.Link, master netlink.Link) error {
	return netlink.LinkSetMaster(link, master)
}

func (o *DefaultNetworkOperator) LinkList() ([]netlink.Link, error) {
	return netlink.LinkList()
}

func (o *DefaultNetworkOperator) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	return netlink.AddrList(link, family)
}

func (o *DefaultNetworkOperator) AddrAdd(link netlink.Link, addr *netlink.Addr) error {
	return netlink.AddrAdd(link, addr)
}

func (o *DefaultNetworkOperator) ParseAddr(s string) (*netlink.Addr, error) {
	return netlink.ParseAddr(s)
}

func (o *DefaultNetworkOperator) RouteList(link netlink.Link, family int) ([]netlink.Route, error) {
	return netlink.RouteList(link, family)
}

func (o *DefaultNetworkOperator) LinkByIndex(index int) (netlink.Link, error) {
	return netlink.LinkByIndex(index)
}

// DefaultNFTablesOperator implements NFTablesOperator using nftables
type DefaultNFTablesOperator struct {
	conn *nftables.Conn
	mu   sync.Mutex
	ctx  context.Context
}

func NewDefaultNFTablesOperator(ctx context.Context) (NFTablesOperator, error) {
	// First verify NFTables subsystem is ready
	if err := checkNFTablesReadiness(ctx); err != nil {
		return nil, fmt.Errorf("NFTables not ready: %w", err)
	}

	conn, err := nftables.New()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to nftables: %w", err)
	}

	return &DefaultNFTablesOperator{
		conn: conn,
		mu:   sync.Mutex{},
		ctx:  ctx,
	}, nil
}

func (op *DefaultNFTablesOperator) ensureConnection() error {
	op.mu.Lock()
	defer op.mu.Unlock()

	// Try current connection first
	if op.conn != nil {
		if _, err := op.conn.ListTables(); err == nil {
			return nil
		}
		// Current connection is invalid
		op.conn = nil
	}

	// Create new connection with retries
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		conn, err := nftables.New()
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt*100) * time.Millisecond)
			continue
		}

		// Verify connection works
		if _, err := conn.ListTables(); err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt*100) * time.Millisecond)
			continue
		}

		op.conn = conn
		return nil
	}

	return fmt.Errorf("failed to establish NFTables connection after retries: %w", lastErr)
}

func (op *DefaultNFTablesOperator) AddTable(table *nftables.Table) error {
	op.mu.Lock()
	defer op.mu.Unlock()

	if op.conn == nil {
		return fmt.Errorf("nftables connection is nil")
	}

	// First check if table already exists
	tables, err := op.conn.ListTables()
	if err != nil {
		return fmt.Errorf("failed to list tables: %v", err)
	}

	for _, t := range tables {
		if t.Name == table.Name && t.Family == table.Family {
			// Table already exists, treat as success
			return nil
		}
	}

	op.conn.AddTable(table)

	// Immediately try to flush to catch any errors
	if err := op.conn.Flush(); err != nil {
		slog.ErrorContext(op.ctx, "Failed to flush after adding table",
			"table", table.Name,
			"error", err)
		return fmt.Errorf("failed to flush after adding table %s: %v", table.Name, err)
	}

	return nil
}

func (op *DefaultNFTablesOperator) DelTable(table *nftables.Table) error {
	if err := op.ensureConnection(); err != nil {
		return err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	op.conn.DelTable(table)
	return nil
}

func (op *DefaultNFTablesOperator) AddChain(chain *nftables.Chain) error {
	if err := op.ensureConnection(); err != nil {
		return err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	op.conn.AddChain(chain)
	return nil
}

func (op *DefaultNFTablesOperator) DelChain(chain *nftables.Chain) error {
	if err := op.ensureConnection(); err != nil {
		return err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	op.conn.DelChain(chain)
	return nil
}

func (op *DefaultNFTablesOperator) AddRule(rule *nftables.Rule) error {
	if err := op.ensureConnection(); err != nil {
		return err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	op.conn.AddRule(rule)
	return nil
}

func (op *DefaultNFTablesOperator) GetRules(table *nftables.Table, chain *nftables.Chain) ([]*nftables.Rule, error) {
	if err := op.ensureConnection(); err != nil {
		return nil, err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.conn.GetRules(table, chain)
}

func (op *DefaultNFTablesOperator) Flush() error {
	if err := op.ensureConnection(); err != nil {
		return err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.conn.Flush()
}

func (op *DefaultNFTablesOperator) Close() {
	op.mu.Lock()
	defer op.mu.Unlock()
	// Just nil the connection as there's no Close method
	op.conn = nil
}

func (op *DefaultNFTablesOperator) GetChains(table *nftables.Table) ([]*nftables.Chain, error) {
	if op.conn == nil {
		return nil, fmt.Errorf("nftables connection is nil")
	}

	// Lock to prevent concurrent access to nftables connection
	op.mu.Lock()
	defer op.mu.Unlock()

	// First check if the table exists
	tables, err := op.conn.ListTables()
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}

	tableExists := false
	for _, t := range tables {
		if t.Name == table.Name && t.Family == table.Family {
			tableExists = true
			break
		}
	}

	if !tableExists {
		return nil, fmt.Errorf("table %s does not exist", table.Name)
	}

	// Get all chains for the specified table
	chains, err := op.conn.ListChains()
	if err != nil {
		return nil, fmt.Errorf("failed to list chains: %w", err)
	}

	// Filter chains for the specified table
	var tableChains []*nftables.Chain
	for _, chain := range chains {
		if chain.Table.Name == table.Name && chain.Table.Family == table.Family {
			tableChains = append(tableChains, chain)
		}
	}

	return tableChains, nil
}

func (op *DefaultNFTablesOperator) FlushChain(chain *nftables.Chain) error {
	if err := op.ensureConnection(); err != nil {
		return err
	}

	op.mu.Lock()
	defer op.mu.Unlock()

	// Get existing rules for the chain
	rules, err := op.conn.GetRules(chain.Table, chain)
	if err != nil {
		return fmt.Errorf("failed to get rules for chain: %w", err)
	}

	// Delete all rules in the chain
	for _, rule := range rules {
		op.conn.DelRule(rule)
	}

	return op.conn.Flush()
}
