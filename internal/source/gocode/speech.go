package gocode

import (
	"context"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"unicode"
)

func commentSpeech(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(group.Text(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "go:") || strings.HasPrefix(line, "+build ") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " ")
}

func identifierSpeech(value string) string {
	runes := []rune(value)
	var words []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	for index, char := range runes {
		if char == '_' || char == '-' || unicode.IsSpace(char) {
			flush()
			continue
		}
		if index > 0 && len(current) > 0 && unicode.IsUpper(char) {
			previous := runes[index-1]
			nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextIsLower) {
				flush()
			}
		}
		current = append(current, char)
	}
	flush()
	return strings.Join(words, " ")
}

func joinNatural(items []string) string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			filtered = append(filtered, item)
		}
	}
	switch len(filtered) {
	case 0:
		return ""
	case 1:
		return filtered[0]
	case 2:
		return filtered[0] + " and " + filtered[1]
	default:
		return strings.Join(filtered[:len(filtered)-1], ", ") + ", and " + filtered[len(filtered)-1]
	}
}

func joinIdentifiers(items []*ast.Ident) string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			names = append(names, identifierSpeech(item.Name))
		}
	}
	return joinNatural(names)
}

func article(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "a value"
	}
	first := strings.ToLower(string([]rune(value)[0]))
	if strings.ContainsRune("aeiou", []rune(first)[0]) {
		return "an " + value
	}
	return "a " + value
}

func typeSpeech(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return identifierSpeech(expression.Name)
	case *ast.SelectorExpr:
		return identifierSpeech(expression.Sel.Name) + " type from " + typeSpeech(expression.X)
	case *ast.StarExpr:
		return "pointer to " + typeSpeech(expression.X)
	case *ast.ArrayType:
		if expression.Len == nil {
			return "slice of " + typeSpeech(expression.Elt)
		}
		length := expressionSpeech(expression.Len)
		if length == "" {
			length = "fixed size"
		}
		return "array of " + length + " " + typeSpeech(expression.Elt)
	case *ast.MapType:
		return "map from " + typeSpeech(expression.Key) + " to " + typeSpeech(expression.Value)
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	case *ast.ChanType:
		prefix := "channel of "
		if expression.Dir == ast.SEND {
			prefix = "send-only channel of "
		} else if expression.Dir == ast.RECV {
			prefix = "receive-only channel of "
		}
		return prefix + typeSpeech(expression.Value)
	case *ast.Ellipsis:
		return "variable number of " + typeSpeech(expression.Elt)
	case *ast.FuncType:
		return functionTypeSpeech(expression)
	case *ast.IndexExpr:
		return typeSpeech(expression.X) + " of " + typeSpeech(expression.Index)
	case *ast.IndexListExpr:
		var args []string
		for _, item := range expression.Indices {
			args = append(args, typeSpeech(item))
		}
		return typeSpeech(expression.X) + " of " + joinNatural(args)
	case *ast.ParenExpr:
		return typeSpeech(expression.X)
	case *ast.UnaryExpr:
		if expression.Op == token.TILDE {
			return "any type with the same underlying representation as " + typeSpeech(expression.X)
		}
	}
	return "value"
}

func typeSummary(spec *ast.TypeSpec) string {
	name := identifierSpeech(spec.Name.Name)
	prefix := "Type " + name
	if params := typeParametersSpeech(spec.TypeParams); params != "" {
		prefix += " parameterized by " + params
	}
	if spec.Assign.IsValid() {
		return prefix + " is an alias for " + article(typeSpeech(spec.Type)) + "."
	}
	switch expression := spec.Type.(type) {
	case *ast.StructType:
		var fields []string
		for _, field := range expression.Fields.List {
			fieldType := typeSpeech(field.Type)
			if len(field.Names) == 0 {
				fields = append(fields, "an embedded "+fieldType)
				continue
			}
			for _, fieldName := range field.Names {
				fields = append(fields, identifierSpeech(fieldName.Name)+" as "+article(fieldType))
			}
		}
		if len(fields) == 0 {
			return prefix + " is an empty struct."
		}
		return prefix + " is a struct with fields " + joinNatural(fields) + "."
	case *ast.InterfaceType:
		var methods []string
		for _, method := range expression.Methods.List {
			if len(method.Names) == 0 {
				methods = append(methods, "the methods of "+typeSpeech(method.Type))
				continue
			}
			for _, methodName := range method.Names {
				description := identifierSpeech(methodName.Name)
				if functionType, ok := method.Type.(*ast.FuncType); ok {
					description += " " + functionTypeSpeech(functionType)
				}
				methods = append(methods, description)
			}
		}
		if len(methods) == 0 {
			return prefix + " is an empty interface."
		}
		return prefix + " is an interface with " + joinNatural(methods) + "."
	default:
		return prefix + " is based on " + article(typeSpeech(spec.Type)) + "."
	}
}

func typeParametersSpeech(fields *ast.FieldList) string {
	if fields == nil {
		return ""
	}
	var params []string
	for _, field := range fields.List {
		constraint := typeSpeech(field.Type)
		if len(field.Names) == 0 {
			params = append(params, "a type constrained by "+constraint)
			continue
		}
		for _, name := range field.Names {
			params = append(params, identifierSpeech(name.Name)+" constrained by "+constraint)
		}
	}
	return joinNatural(params)
}

func functionSummary(declaration *ast.FuncDecl) string {
	name := identifierSpeech(declaration.Name.Name)
	subject := "Function " + name
	if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
		receiverType := typeSpeech(declaration.Recv.List[0].Type)
		subject = "Method " + name + " on " + article(receiverType)
	}
	if params := typeParametersSpeech(declaration.Type.TypeParams); params != "" {
		subject += " parameterized by " + params
	}
	var sentences []string
	parameters := fieldDescriptions(declaration.Type.Params)
	if len(parameters) > 0 {
		sentences = append(sentences, "accepts "+joinNatural(parameters))
	}
	results := fieldDescriptions(declaration.Type.Results)
	if len(results) > 0 {
		sentences = append(sentences, "returns "+joinNatural(results))
	}
	if len(sentences) == 0 {
		return subject + " has no parameters and returns no values."
	}
	return subject + " " + strings.Join(sentences, ". It ") + "."
}

func functionTypeSpeech(function *ast.FuncType) string {
	parameters := fieldDescriptions(function.Params)
	results := fieldDescriptions(function.Results)
	var clauses []string
	if len(parameters) > 0 {
		clauses = append(clauses, "accepting "+joinNatural(parameters))
	}
	if len(results) > 0 {
		clauses = append(clauses, "returning "+joinNatural(results))
	}
	if len(clauses) == 0 {
		return "function with no parameters or results"
	}
	return "function " + strings.Join(clauses, " and ")
}

func fieldDescriptions(fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	var descriptions []string
	for _, field := range fields.List {
		typ := typeSpeech(field.Type)
		if len(field.Names) == 0 {
			descriptions = append(descriptions, article(typ))
			continue
		}
		names := joinIdentifiers(field.Names)
		if strings.HasPrefix(typ, "variable number of ") {
			descriptions = append(descriptions, names+" as a "+strings.TrimPrefix(typ, "variable number of ")+" variadic argument")
		} else {
			descriptions = append(descriptions, names+" as "+article(typ))
		}
	}
	return descriptions
}

func valueSummary(tokenKind token.Token, spec *ast.ValueSpec) string {
	names := make([]string, 0, len(spec.Names))
	for _, name := range spec.Names {
		names = append(names, identifierSpeech(name.Name))
	}
	joinedNames := joinNatural(names)
	if len(spec.Values) > 0 {
		var values []string
		for _, value := range spec.Values {
			values = append(values, expressionSpeech(value))
		}
		if tokenKind == token.CONST {
			return "Constant " + joinedNames + " has value " + joinNatural(values) + "."
		}
		return "Variable " + joinedNames + " is initialized with " + joinNatural(values) + "."
	}
	if spec.Type != nil {
		if tokenKind == token.CONST {
			return "Constant " + joinedNames + " is declared with type " + typeSpeech(spec.Type) + "."
		}
		return "Variable " + joinedNames + " has type " + typeSpeech(spec.Type) + "."
	}
	return capitalize(tokenKind.String()) + " " + joinedNames + " is declared in this group."
}

func capitalize(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func bodyNarration(ctx context.Context, body *ast.BlockStmt) ([]bodyNarrative, error) {
	if body == nil {
		return nil, nil
	}
	var narration []bodyNarrative
	for _, statement := range body.List {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		texts, err := statementNarration(ctx, statement)
		if err != nil {
			return nil, err
		}
		for _, text := range texts {
			narration = append(narration, bodyNarrative{text: text, statement: statement})
		}
	}
	return narration, nil
}

type bodyNarrative struct {
	text      string
	statement ast.Stmt
}

func statementNarration(ctx context.Context, statement ast.Stmt) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch statement := statement.(type) {
	case *ast.ReturnStmt:
		if len(statement.Results) == 0 {
			return []string{"It exits the function."}, nil
		}
		values := expressionList(statement.Results)
		if values == "" {
			return nil, nil
		}
		return []string{"It returns " + values + "."}, nil
	case *ast.IfStmt:
		condition := expressionSpeech(statement.Cond)
		if condition == "" {
			condition = "the condition is true"
		}
		thenItems, err := statementNarration(ctx, statement.Body)
		if err != nil {
			return nil, err
		}
		then := actionPhrase(thenItems)
		if then == "" {
			then = "continues"
		}
		result := []string{"If " + condition + ", it " + then + "."}
		if statement.Else != nil {
			otherwiseItems, err := statementNarration(ctx, statement.Else)
			if err != nil {
				return nil, err
			}
			otherwise := actionPhrase(otherwiseItems)
			if otherwise != "" {
				result = append(result, "Otherwise, it "+otherwise+".")
			}
		}
		return result, nil
	case *ast.ForStmt:
		condition := "the loop has not been stopped"
		if statement.Cond != nil {
			condition = expressionSpeech(statement.Cond)
		}
		bodyItems, err := statementNarration(ctx, statement.Body)
		if err != nil {
			return nil, err
		}
		body := actionPhrase(bodyItems)
		if body == "" {
			return []string{"It repeats while " + condition + "."}, nil
		}
		return []string{"It repeats while " + condition + " and " + body + "."}, nil
	case *ast.RangeStmt:
		value := expressionSpeech(statement.X)
		if value == "" {
			return nil, nil
		}
		variables := rangeVariables(statement)
		bodyItems, err := statementNarration(ctx, statement.Body)
		if err != nil {
			return nil, err
		}
		body := actionPhrase(bodyItems)
		if body == "" {
			return []string{"It processes each " + variables + " in " + value + "."}, nil
		}
		return []string{"For each " + variables + " in " + value + ", it " + body + "."}, nil
	case *ast.SwitchStmt:
		return switchNarration(ctx, statement.Tag, statement.Body.List)
	case *ast.TypeSwitchStmt:
		return []string{"It selects behavior based on the type of " + typeSwitchSubject(statement.Assign) + "."}, nil
	case *ast.SelectStmt:
		var clauses []string
		for _, item := range statement.Body.List {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			clause, ok := item.(*ast.CommClause)
			if !ok {
				continue
			}
			label := "a channel operation"
			if clause.Comm == nil {
				label = "the default case"
			}
			actionItems, err := statementNarrations(ctx, clause.Body)
			if err != nil {
				return nil, err
			}
			actions := actionPhrase(actionItems)
			if actions != "" {
				clauses = append(clauses, label+", it "+actions)
			}
		}
		if len(clauses) == 0 {
			return []string{"It waits for one of several channel operations."}, nil
		}
		return []string{"It waits for one of several channel operations: " + joinNatural(clauses) + "."}, nil
	case *ast.ExprStmt:
		if call, ok := statement.X.(*ast.CallExpr); ok {
			if summary := callSpeech(call); summary != "" {
				return []string{"It " + summary + "."}, nil
			}
		}
		if receive, ok := statement.X.(*ast.UnaryExpr); ok && receive.Op == token.ARROW {
			return []string{"It receives from " + expressionSpeech(receive.X) + "."}, nil
		}
		return nil, nil
	case *ast.AssignStmt:
		left := expressionList(statement.Lhs)
		right := expressionList(statement.Rhs)
		if left != "" && right != "" {
			return []string{"It assigns " + right + " to " + left + "."}, nil
		}
		return nil, nil
	case *ast.IncDecStmt:
		verb := "increments"
		if statement.Tok == token.DEC {
			verb = "decrements"
		}
		return []string{"It " + verb + " " + expressionSpeech(statement.X) + "."}, nil
	case *ast.GoStmt:
		if call := callSpeech(statement.Call); call != "" {
			return []string{"It starts " + call + " in a goroutine."}, nil
		}
		return []string{"It starts a goroutine."}, nil
	case *ast.DeferStmt:
		if call := callSpeech(statement.Call); call != "" {
			return []string{"It defers " + call + " until the function returns."}, nil
		}
		return []string{"It defers an operation until the function returns."}, nil
	case *ast.SendStmt:
		return []string{"It sends " + expressionSpeech(statement.Value) + " to " + expressionSpeech(statement.Chan) + "."}, nil
	case *ast.BranchStmt:
		switch statement.Tok {
		case token.BREAK:
			return []string{"It exits the current loop or switch."}, nil
		case token.CONTINUE:
			return []string{"It continues with the next loop iteration."}, nil
		case token.FALLTHROUGH:
			return []string{"It continues into the next switch case."}, nil
		}
	case *ast.BlockStmt:
		return statementNarrations(ctx, statement.List)
	case *ast.LabeledStmt:
		return statementNarration(ctx, statement.Stmt)
	case *ast.DeclStmt:
		declaration, ok := statement.Decl.(*ast.GenDecl)
		if !ok || len(declaration.Specs) == 0 {
			return nil, nil
		}
		if value, ok := declaration.Specs[0].(*ast.ValueSpec); ok {
			return []string{valueSummary(declaration.Tok, value)}, nil
		}
	}
	return nil, nil
}

func statementNarrations(ctx context.Context, statements []ast.Stmt) ([]string, error) {
	var result []string
	for _, statement := range statements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		narration, err := statementNarration(ctx, statement)
		if err != nil {
			return nil, err
		}
		result = append(result, narration...)
	}
	return result, nil
}

func switchNarration(ctx context.Context, tag ast.Expr, statements []ast.Stmt) ([]string, error) {
	intro := "It selects behavior"
	if tag != nil {
		if spoken := expressionSpeech(tag); spoken != "" {
			intro += " based on " + spoken
		}
	}
	var cases []string
	for _, statement := range statements {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clause, ok := statement.(*ast.CaseClause)
		if !ok {
			continue
		}
		label := "the default case"
		if len(clause.List) > 0 {
			label = "case " + expressionList(clause.List)
		}
		actionItems, err := statementNarrations(ctx, clause.Body)
		if err != nil {
			return nil, err
		}
		actions := actionPhrase(actionItems)
		if actions != "" {
			cases = append(cases, label+", it "+actions)
		} else {
			cases = append(cases, label)
		}
	}
	if len(cases) > 0 {
		intro += ": " + joinNatural(cases)
	}
	return []string{intro + "."}, nil
}

func typeSwitchSubject(statement ast.Stmt) string {
	switch statement := statement.(type) {
	case *ast.AssignStmt:
		if len(statement.Rhs) > 0 {
			return expressionSpeech(statement.Rhs[0])
		}
	case *ast.ExprStmt:
		return expressionSpeech(statement.X)
	}
	return "the checked value"
}

func rangeVariables(statement *ast.RangeStmt) string {
	var variables []string
	if id, ok := statement.Key.(*ast.Ident); ok && id.Name != "_" {
		variables = append(variables, identifierSpeech(id.Name))
	}
	if id, ok := statement.Value.(*ast.Ident); ok && id.Name != "_" {
		variables = append(variables, identifierSpeech(id.Name))
	}
	if len(variables) == 0 {
		return "each item"
	}
	return joinNatural(variables)
}

func actionPhrase(sentences []string) string {
	var actions []string
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(strings.TrimSuffix(sentence, "."))
		sentence = strings.TrimPrefix(sentence, "It ")
		sentence = strings.TrimPrefix(sentence, "it ")
		if sentence != "" {
			actions = append(actions, sentence)
		}
	}
	return joinNatural(actions)
}

func callSpeech(call *ast.CallExpr) string {
	return callPhrase(call, false)
}

func callExpressionSpeech(call *ast.CallExpr) string {
	if name, ok := call.Fun.(*ast.Ident); ok && len(call.Args) == 1 {
		switch name.Name {
		case "len":
			return "the length of " + expressionSpeech(call.Args[0])
		case "cap":
			return "the capacity of " + expressionSpeech(call.Args[0])
		}
	}
	if function, ok := call.Fun.(*ast.SelectorExpr); ok {
		receiver := expressionSpeech(function.X)
		switch function.Sel.Name {
		case "Err":
			if len(call.Args) == 0 {
				return "the error reported by " + receiver
			}
		case "New":
			if receiver == "errors" && len(call.Args) == 1 {
				return "a new error containing " + expressionSpeech(call.Args[0])
			}
		case "ReadAll":
			if receiver == "io" && len(call.Args) == 1 {
				return "all bytes read from " + expressionSpeech(call.Args[0])
			}
		}
	}
	return callPhrase(call, true)
}

func callPhrase(call *ast.CallExpr, asExpression bool) string {
	name := "the function"
	receiver := ""
	switch function := call.Fun.(type) {
	case *ast.Ident:
		name = identifierSpeech(function.Name)
	case *ast.SelectorExpr:
		name = identifierSpeech(function.Sel.Name)
		receiver = expressionSpeech(function.X)
	case *ast.IndexExpr:
		name = expressionSpeech(function.X)
	case *ast.IndexListExpr:
		name = expressionSpeech(function.X)
	case *ast.FuncLit:
		name = "an anonymous function"
	default:
		if spoken := expressionSpeech(call.Fun); spoken != "" {
			name = spoken
		}
	}
	phrase := "calls " + name
	if asExpression {
		phrase = "the result of calling " + name
	}
	if receiver != "" {
		phrase += " on " + receiver
	}
	if len(call.Args) > 0 {
		args := expressionList(call.Args)
		if args != "" {
			phrase += " with " + args
		}
	}
	return phrase
}

func expressionList(expressions []ast.Expr) string {
	var items []string
	for _, expression := range expressions {
		if spoken := expressionSpeech(expression); spoken != "" {
			items = append(items, spoken)
		}
	}
	return joinNatural(items)
}

func expressionSpeech(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		switch expression.Name {
		case "nil":
			return "nil"
		case "true":
			return "true"
		case "false":
			return "false"
		case "iota":
			return "the current constant index"
		case "_":
			return "the discarded value"
		default:
			return identifierSpeech(expression.Name)
		}
	case *ast.BasicLit:
		switch expression.Kind {
		case token.STRING:
			value, err := strconv.Unquote(expression.Value)
			if err != nil || value == "" {
				return "an empty string"
			}
			return "the text " + value
		case token.CHAR:
			value, err := strconv.Unquote(expression.Value)
			if err == nil {
				return "the character " + value
			}
		}
		return expression.Value
	case *ast.SelectorExpr:
		base := expressionSpeech(expression.X)
		member := identifierSpeech(expression.Sel.Name)
		if base == "" {
			return member
		}
		return member + " on " + base
	case *ast.CallExpr:
		return callExpressionSpeech(expression)
	case *ast.ParenExpr:
		return expressionSpeech(expression.X)
	case *ast.BinaryExpr:
		left, right := expressionSpeech(expression.X), expressionSpeech(expression.Y)
		if left == "" || right == "" {
			return ""
		}
		operator := map[token.Token]string{
			token.EQL: "equals", token.NEQ: "does not equal", token.LSS: "is less than",
			token.LEQ: "is at most", token.GTR: "is greater than", token.GEQ: "is at least",
			token.LAND: "and", token.LOR: "or", token.ADD: "plus", token.SUB: "minus",
			token.MUL: "times", token.QUO: "divided by", token.REM: "remainder after dividing by",
			token.AND: "bitwise and", token.OR: "bitwise or", token.XOR: "bitwise exclusive or",
			token.SHL: "shifted left by", token.SHR: "shifted right by", token.AND_NOT: "with bits removed by",
		}[expression.Op]
		if operator == "" {
			return ""
		}
		return left + " " + operator + " " + right
	case *ast.UnaryExpr:
		value := expressionSpeech(expression.X)
		switch expression.Op {
		case token.NOT:
			return "not " + value
		case token.SUB:
			return "negative " + value
		case token.ADD:
			return "positive " + value
		case token.AND:
			return "the address of " + value
		case token.MUL:
			return "the value pointed to by " + value
		case token.ARROW:
			return "a value received from " + value
		}
	case *ast.IndexExpr:
		return "the item at index " + expressionSpeech(expression.Index) + " in " + expressionSpeech(expression.X)
	case *ast.SliceExpr:
		return "a slice of " + expressionSpeech(expression.X)
	case *ast.TypeAssertExpr:
		return "the value as " + typeSpeech(expression.Type)
	case *ast.CompositeLit:
		return "a value of type " + typeSpeech(expression.Type)
	case *ast.KeyValueExpr:
		return expressionSpeech(expression.Key) + " with value " + expressionSpeech(expression.Value)
	case *ast.FuncLit:
		return "an anonymous function"
	case *ast.StarExpr:
		return "the value pointed to by " + expressionSpeech(expression.X)
	case *ast.Ellipsis:
		return "all values in " + expressionSpeech(expression.Elt)
	}
	return ""
}
