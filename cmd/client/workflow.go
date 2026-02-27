package main

import (
	"bufio"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

type importWorkflowResult struct {
	summary             processingSummary
	verificationTargets map[string]verificationTarget
	censusMemberIDs     []string
}

type processMemberResult struct {
	Member  apicommon.OrgMember
	Updated bool
	Skipped bool
}

// runMemberImport reads the CSV and executes row-level member upsert workflow.
func runMemberImport(
	client *Client,
	cfg Config,
	org *organizationContext,
	stdin *bufio.Reader,
) (*importWorkflowResult, error) {
	rows, err := readCSV(cfg.CSVPath, cfg.IDField)
	if err != nil {
		return nil, fmt.Errorf("read csv %s: %w", cfg.CSVPath, err)
	}

	summary, verificationTargets, censusMemberIDs := processRows(client, cfg, org, rows, stdin)
	return &importWorkflowResult{
		summary:             summary,
		verificationTargets: verificationTargets,
		censusMemberIDs:     censusMemberIDs,
	}, nil
}

// processRows processes each CSV row against organization members and returns
// counters, expected verification values and member IDs to add to census.
func processRows(
	client *Client,
	cfg Config,
	org *organizationContext,
	rows []csvRow,
	stdin *bufio.Reader,
) (processingSummary, map[string]verificationTarget, []string) {
	summary := processingSummary{TotalRows: len(rows)}
	verificationTargets := make(map[string]verificationTarget)
	censusMemberIDs := map[string]struct{}{}

	for _, row := range rows {
		if strings.TrimSpace(row.Identifier) == "" {
			summary.Skipped++
			summary.Errors++
			fmt.Printf("row %d skipped: empty %s\n", row.Line, cfg.IDField)
			continue
		}

		existingMember, err := findMemberByIdentifier(client, org.Address, cfg.IDField, row.Identifier)
		if err != nil {
			summary.Errors++
			fmt.Printf("row %d error finding member %s=%s: %v\n", row.Line, cfg.IDField, row.Identifier, err)
			continue
		}

		rowResult, processErr := processMemberRow(
			client,
			cfg,
			org,
			row,
			existingMember,
			stdin,
		)
		if processErr != nil {
			summary.Errors++
			fmt.Printf("row %d process failed for %s=%s: %v\n", row.Line, cfg.IDField, row.Identifier, processErr)
			continue
		}
		member := rowResult.Member
		if member.ID == "" {
			summary.Errors++
			fmt.Printf("row %d invalid member state for %s=%s: empty member ID\n", row.Line, cfg.IDField, row.Identifier)
			continue
		}

		if rowResult.Updated {
			summary.Updated++
		}
		if rowResult.Skipped {
			summary.Skipped++
		}
		if existingMember == nil {
			summary.Created++
		}

		censusMemberIDs[member.ID] = struct{}{}
		target, targetErr := verificationTargetFromMember(row.Identifier, member, org)
		if targetErr != nil {
			summary.Errors++
			fmt.Printf("row %d verification setup failed for %s=%s: %v\n", row.Line, cfg.IDField, row.Identifier, targetErr)
			continue
		}
		verificationTargets[member.ID] = target
	}

	return summary, verificationTargets, sortedMemberIDs(censusMemberIDs)
}

func processMemberRow(
	client *Client,
	cfg Config,
	org *organizationContext,
	row csvRow,
	existing *apicommon.OrgMember,
	stdin *bufio.Reader,
) (processMemberResult, error) {
	if existing == nil {
		member, err := createMemberFromRow(client, org, cfg, row)
		if err != nil {
			return processMemberResult{}, err
		}
		return processMemberResult{Member: member}, nil
	}

	result, err := updateExistingMemberFromRow(
		client,
		org,
		cfg,
		row,
		existing,
		stdin,
	)
	if err != nil {
		return processMemberResult{}, err
	}
	return result, nil
}

func createMemberFromRow(client *Client, org *organizationContext, cfg Config, row csvRow) (apicommon.OrgMember, error) {
	member := apicommon.OrgMember{}
	applyCSVValues(&member, row.Values)

	memberID, err := client.upsertMember(org.Address, member)
	if err != nil {
		return apicommon.OrgMember{}, fmt.Errorf("create failed for %s=%s: %w", cfg.IDField, row.Identifier, err)
	}
	member.ID = memberID
	fmt.Printf("row %d created member %s=%s (id=%s)\n", row.Line, cfg.IDField, row.Identifier, memberID)
	return member, nil
}

func updateExistingMemberFromRow(
	client *Client,
	org *organizationContext,
	cfg Config,
	row csvRow,
	existing *apicommon.OrgMember,
	stdin *bufio.Reader,
) (processMemberResult, error) {
	updatedMember := *existing
	changedPhone, err := hasPhoneChanges(row.Values, existing, org)
	if err != nil {
		return processMemberResult{}, fmt.Errorf("cannot compare contact data: %w", err)
	}

	applyCSVValues(&updatedMember, row.Values)

	toUpdateStr := ""
	toUpdateStr += fmt.Sprintf("---\nrow %d existing member: %v\n", row.Line, existing)
	if !changedPhone {
		toUpdateStr += fmt.Sprintf(
			"row %d no phone changes detected for %s=%s, preserving existing phone\n",
			row.Line,
			cfg.IDField,
			row.Identifier,
		)
		updatedMember.Phone = ""
	}
	toUpdateStr += fmt.Sprintf("row %d new values: %v\n", row.Line, updatedMember)

	update := cfg.Yes
	if cfg.Yes {
		fmt.Printf("%sUpdate info for %s? [Y/N] Y (auto-approved by --yes)\n", toUpdateStr, row.Identifier)
	} else {
		promptUpdate, promptErr := promptYesNo(
			stdin,
			fmt.Sprintf("%sUpdate info for %s? [Y/N] ", toUpdateStr, row.Identifier),
		)
		if promptErr != nil {
			return processMemberResult{}, fmt.Errorf("prompt failed: %w", promptErr)
		}
		update = promptUpdate
	}

	if !update {
		if !changedPhone {
			updatedMember.Phone = existing.Phone
		}
		fmt.Printf("row %d skipped update for %s=%s\n", row.Line, cfg.IDField, row.Identifier)
		return processMemberResult{
			Member:  updatedMember,
			Skipped: true,
		}, nil
	}

	memberID, upsertErr := client.upsertMember(org.Address, updatedMember)
	if upsertErr != nil {
		return processMemberResult{}, fmt.Errorf("update failed: %w", upsertErr)
	}
	updatedMember.ID = memberID
	// reuse original phone for expected verification
	if !changedPhone {
		updatedMember.Phone = existing.Phone
	}
	fmt.Printf("row %d updated member %s=%s (id=%s)\n", row.Line, cfg.IDField, row.Identifier, memberID)
	return processMemberResult{
		Member:  updatedMember,
		Updated: true,
	}, nil
}

func hasPhoneChanges(values map[string]string, existing *apicommon.OrgMember, org *organizationContext) (bool, error) {
	phoneChanged := false
	if csvPhone, ok := values["phone"]; ok {
		masked, err := maskedPhone(csvPhone, org.Address, org.Country)
		if err != nil {
			return false, fmt.Errorf("normalize phone: %w", err)
		}
		fmt.Printf("comparing phone, csv: %s, masked csv: %s, existing: %s\n", csvPhone, masked, existing.Phone)
		phoneChanged = !strings.EqualFold(masked, existing.Phone)
		fmt.Printf("phone changed: %v\n", phoneChanged)
	}

	return phoneChanged, nil
}

func verificationTargetFromMember(
	identifier string,
	member apicommon.OrgMember,
	org *organizationContext,
) (verificationTarget, error) {
	target := verificationTarget{
		Identifier:    identifier,
		MemberID:      member.ID,
		ExpectedEmail: member.Email,
		ExpectedPhone: member.Phone,
	}

	if member.Phone != "" && !isMaskedPhone(member.Phone) {
		masked, err := maskedPhone(member.Phone, org.Address, org.Country)
		if err != nil {
			return verificationTarget{}, fmt.Errorf("prepare phone verification target: %w", err)
		}
		target.ExpectedPhone = masked
	}
	return target, nil
}

func applyCSVValues(member *apicommon.OrgMember, values map[string]string) {
	for field, value := range values {
		switch field {
		case "name":
			member.Name = value
		case "surname":
			member.Surname = value
		case "birthDate":
			member.BirthDate = value
		case "email":
			member.Email = value
		case "phone":
			member.Phone = value
		case "password":
			member.Password = value
		case "weight":
			member.Weight = value
		case "nationalId":
			member.NationalID = value
		case "memberNumber":
			member.MemberNumber = value
		default:
			// Ignore unsupported keys.
		}
	}
}

func findMemberByIdentifier(client *Client, orgAddress common.Address, idField, identifier string) (*apicommon.OrgMember, error) {
	page := 1
	limit := 100
	var found *apicommon.OrgMember

	for {
		resp, err := client.organizationMembers(orgAddress, identifier, page, limit)
		if err != nil {
			return nil, fmt.Errorf("search members page %d: %w", page, err)
		}

		for _, member := range resp.Members {
			if !memberMatchesIdentifier(member, idField, identifier) {
				continue
			}
			if found != nil && found.ID != member.ID {
				return nil, fmt.Errorf("multiple members matched %s=%s", idField, identifier)
			}
			copyMember := member
			found = &copyMember
		}

		if resp.Pagination == nil || resp.Pagination.NextPage == nil {
			break
		}
		page = int(*resp.Pagination.NextPage)
		if page <= 0 {
			break
		}
	}

	return found, nil
}

func memberMatchesIdentifier(member apicommon.OrgMember, idField, identifier string) bool {
	switch idField {
	case identifierFieldNationalID:
		return strings.TrimSpace(member.NationalID) == identifier
	case identifierFieldMemberNumber:
		return strings.TrimSpace(member.MemberNumber) == identifier
	default:
		return false
	}
}

func addMembersToCensus(client *Client, censusID string, memberIDs []string) (int, []string, error) {
	if len(memberIDs) == 0 {
		fmt.Println("No member IDs available to add to census.")
		return 0, nil, nil
	}

	resp, err := client.addMembersToCensus(censusID, memberIDs)
	if err != nil {
		return 0, nil, fmt.Errorf("add members to census %s: %w", censusID, err)
	}
	fmt.Printf("Added %d members to census %s\n", resp.Added, censusID)
	return int(resp.Added), resp.Errors, nil
}

func sortedMemberIDs(memberIDs map[string]struct{}) []string {
	ids := make([]string, 0, len(memberIDs))
	for memberID := range memberIDs {
		ids = append(ids, memberID)
	}
	sort.Strings(ids)
	return ids
}

func isMaskedPhone(phone string) bool {
	return strings.HasSuffix(phone, "***")
}
