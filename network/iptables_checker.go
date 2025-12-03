//go:build linux

package network

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/google/nftables"
	"github.com/gookit/color"
	"github.com/mattn/go-isatty"
)

// PolicyStatus represents the status of the iptables forward policy check
type PolicyStatus struct {
	IsHealthy bool
	// Message contains the error message to be displayed to the user when not healthy
	Message string
}

// IptablesChecker provides functionality to check iptables forward policy
type IptablesChecker struct {
	nftOp                      NFTablesOperator
	onPolicyChange             func(status PolicyStatus) // Callback for policy status changes
	lastKnownStatus            *PolicyStatus             // Track last reported status to avoid duplicate reports
	verboseInstructionsPrinted bool                      // Track if verbose instructions have been printed
}

// NewIptablesChecker creates a new IptablesChecker instance
func NewIptablesChecker(nftOp NFTablesOperator, onPolicyChange func(status PolicyStatus)) *IptablesChecker {
	return &IptablesChecker{
		nftOp:          nftOp,
		onPolicyChange: onPolicyChange,
	}
}

// CheckForwardPolicy verifies that the iptables FORWARD chain has rules to allow beacon0 traffic
// If the policy is DROP and our rules are missing, it adds them automatically
func (ic *IptablesChecker) CheckForwardPolicy(ctx context.Context) error {
	slog.DebugContext(ctx, "Checking iptables FORWARD chain policy")

	var newStatus PolicyStatus

	// Check the standard filter table FORWARD chain policy
	// This works for both modern nftables and legacy iptables since they share the same namespace
	if err := ic.checkForwardPolicyInFilterTable(ctx); err != nil {
		// Policy is not set to ACCEPT, try to add specific rules for beacon0
		slog.InfoContext(ctx, "FORWARD chain policy is not ACCEPT, adding specific rules for beacon0", "error", err)

		if err := ic.ensureBeaconForwardRules(ctx); err != nil {
			errMsg := "Failed to add iptables FORWARD rules for beacon0 - environments may fail to start or lose network connectivity."
			slog.WarnContext(ctx, errMsg, "error", err)

			// Print verbose instructions the first time this happens during runner execution
			if !ic.verboseInstructionsPrinted {
				ic.printVerboseInstructions()
				ic.verboseInstructionsPrinted = true
			}

			newStatus = PolicyStatus{
				IsHealthy: false,
				Message:   errMsg,
			}
		} else {
			slog.InfoContext(ctx, "Successfully added iptables FORWARD rules for beacon0")
			newStatus = PolicyStatus{
				IsHealthy: true,
			}
		}
	} else {
		slog.DebugContext(ctx, "FORWARD chain policy is correctly set to ACCEPT")
		newStatus = PolicyStatus{
			IsHealthy: true,
		}
	}

	// Only report status change if different from last known status
	if ic.shouldReportStatusChange(newStatus) {
		if ic.onPolicyChange != nil {
			ic.onPolicyChange(newStatus)
		}
		ic.lastKnownStatus = &newStatus
	}

	if !newStatus.IsHealthy {
		return fmt.Errorf("iptables FORWARD policy check failed")
	}

	return nil
}

// ensureBeaconForwardRules adds iptables rules to allow traffic from/to beacon0
// Since nftables 'accept' verdicts don't prevent iptables-nft chains from also evaluating,
// we need to add rules to the iptables filter FORWARD chain as well
func (ic *IptablesChecker) ensureBeaconForwardRules(ctx context.Context) error {
	// Check if rules already exist
	if ic.hasBeaconForwardRules(ctx) {
		slog.DebugContext(ctx, "beacon0 FORWARD rules already exist")
		return nil
	}

	// Add rules to allow beacon0 traffic through the FORWARD chain
	// Insert at position 1 to run before any DROP rules
	rules := [][]string{
		{"-I", "FORWARD", "1", "-i", bridgeName, "-j", "ACCEPT"},
		{"-I", "FORWARD", "2", "-o", bridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	for _, rule := range rules {
		cmd := exec.CommandContext(ctx, "iptables", rule...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to add iptables rule %v: %w (output: %s)", rule, err, string(output))
		}
		slog.InfoContext(ctx, "Added iptables FORWARD rule for beacon0", "rule", strings.Join(rule, " "))
	}

	return nil
}

// hasBeaconForwardRules checks if beacon0 rules already exist in FORWARD chain
func (ic *IptablesChecker) hasBeaconForwardRules(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "iptables", "-L", "FORWARD", "-n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.DebugContext(ctx, "Failed to check existing iptables FORWARD rules", "error", err)
		return false
	}

	// Check if we have rules for beacon0 in FORWARD chain
	outputStr := string(output)
	hasInRule := strings.Contains(outputStr, fmt.Sprintf("ACCEPT     all  --  %s", bridgeName))
	hasOutRule := strings.Contains(outputStr, fmt.Sprintf("ACCEPT     all  --  *      %s", bridgeName))

	return hasInRule && hasOutRule
}

// CleanupBeaconForwardRules removes the beacon0 rules from FORWARD chain
// This should be called when shutting down the network manager
func (ic *IptablesChecker) CleanupBeaconForwardRules(ctx context.Context) error {
	if !ic.hasBeaconForwardRules(ctx) {
		return nil
	}

	// Delete rules from FORWARD chain
	rules := [][]string{
		{"-D", "FORWARD", "-o", bridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-D", "FORWARD", "-i", bridgeName, "-j", "ACCEPT"},
	}

	for _, rule := range rules {
		cmd := exec.CommandContext(ctx, "iptables", rule...)
		if output, err := cmd.CombinedOutput(); err != nil {
			slog.WarnContext(ctx, "Failed to remove iptables FORWARD rule", "rule", strings.Join(rule, " "), "error", err, "output", string(output))
		} else {
			slog.InfoContext(ctx, "Removed iptables FORWARD rule for beacon0", "rule", strings.Join(rule, " "))
		}
	}

	return nil
}

// shouldReportStatusChange determines if we should report a status change
func (ic *IptablesChecker) shouldReportStatusChange(newStatus PolicyStatus) bool {
	if ic.lastKnownStatus == nil {
		return true // First check, always report
	}

	// Report if health status changed
	return ic.lastKnownStatus.IsHealthy != newStatus.IsHealthy || ic.lastKnownStatus.Message != newStatus.Message
}

// checkForwardPolicyInFilterTable checks the FORWARD chain policy in the filter table
// This works for both modern nftables and legacy iptables since nftables can access
// the iptables compatibility layer through the same interface
func (ic *IptablesChecker) checkForwardPolicyInFilterTable(ctx context.Context) error {
	if ic.nftOp == nil {
		return fmt.Errorf("nftables operator not available")
	}

	// Get all chains from the filter table (IPv4)
	// This covers both native nftables and iptables-compat chains
	filterTable := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   "filter",
	}

	chains, err := ic.nftOp.GetChains(filterTable)
	if err != nil {
		slog.DebugContext(ctx, "Failed to get filter table chains", "error", err)
		return fmt.Errorf("failed to get filter table chains: %w", err)
	}

	// Find the FORWARD chain and check its policy
	for _, chain := range chains {
		if chain.Name == "FORWARD" && chain.Table.Family == nftables.TableFamilyIPv4 && chain.Table.Name == "filter" {
			// Check if policy is nil (no policy set) or not ACCEPT
			if chain.Policy == nil {
				return fmt.Errorf("FORWARD chain has no policy set")
			}

			policy := nftables.ChainPolicyAccept
			if *chain.Policy != policy {
				return fmt.Errorf("FORWARD chain policy is not set to ACCEPT")
			}

			return nil // Policy is ACCEPT
		}
	}

	return fmt.Errorf("FORWARD chain not found in filter table")
}

// printVerboseInstructions prints detailed instructions on how to fix the iptables FORWARD policy
func (ic *IptablesChecker) printVerboseInstructions() {
	fmt.Println("\n⚠️  Iptables FORWARD Policy Configuration Needed:")
	fmt.Println("  • Gitpod environments run in virtual machines that need network connectivity")
	fmt.Println("  • The iptables FORWARD chain policy must be set to ACCEPT to allow traffic between VMs and your network")
	fmt.Println("  • Without this setting, environments would be unable to access the internet or communicate with the runner properly")
	fmt.Println()
	fmt.Println("📋 Configuration Methods:")
	fmt.Println()
	fmt.Println("Method 1 - Using iptables directly:")
	fmt.Printf("     %s\n", boldString("sudo iptables -P FORWARD ACCEPT"))
	fmt.Println()
	fmt.Println("Method 2 - Using nftables (modern systems):")
	fmt.Printf("     %s\n", boldString("sudo nft chain ip filter FORWARD '{ policy accept; }'"))
	fmt.Println()
	fmt.Println("✅ How to verify the change:")
	fmt.Printf("     %s\n", boldString("sudo iptables -L FORWARD | head -1"))
	fmt.Println("     Should show: Chain FORWARD (policy ACCEPT)")
	fmt.Println()
	fmt.Println("⏰ After applying changes:")
	fmt.Println("  • The runner will automatically detect the fix within 1 minute")
	fmt.Println("  • To clear the degradation message immediately: restart the runner")
	fmt.Println()
	fmt.Println("💾 Making it permanent:")
	fmt.Printf("  • For iptables: Save rules with your distribution's method (e.g., %s)\n", boldString("iptables-save"))
	fmt.Printf("  • For nftables: Add rules to %s\n", boldString("/etc/nftables.conf"))
	fmt.Println("  • For systemd systems: Create a systemd service to set the policy on boot")
	fmt.Println()
	fmt.Println("🔄 How to revert later (if you uninstall the Gitpod runner):")
	fmt.Printf("  1. For iptables: %s\n", boldString("sudo iptables -P FORWARD DROP"))
	fmt.Printf("  2. For nftables: %s\n", boldString("sudo nft chain ip filter FORWARD '{ policy drop; }'"))
	fmt.Println()
}

// boldString returns a bold-formatted string if color output is supported
func boldString(msg string) string {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return msg
	}
	return color.Bold.Sprint(msg)
}
