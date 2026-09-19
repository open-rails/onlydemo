package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/authkit/authhttp"
	"github.com/open-rails/authkit/verify"
)

type blogAPI struct {
	pool *pgxpool.Pool
	auth *authhttp.Service
}

type blogPost struct {
	ID         int64     `json:"id"`
	Slug       string    `json:"slug"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Visibility string    `json:"visibility"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type blogPostInput struct {
	Slug       *string `json:"slug"`
	Title      *string `json:"title"`
	Body       *string `json:"body"`
	Visibility *string `json:"visibility"`
}

func (api *blogAPI) list(c fiber.Ctx) error {
	ownerID, err := api.optionalUserID(c)
	if err != nil {
		return authError(c, err)
	}

	query := `
		SELECT id, slug, title, body, visibility, created_at, updated_at
		FROM blog_posts
		WHERE visibility = 'public'`
	args := []any{}
	if ownerID != "" {
		query += " OR owner_id = $1"
		args = append(args, ownerID)
	}
	query += " ORDER BY created_at DESC"

	rows, err := api.pool.Query(c.Context(), query, args...)
	if err != nil {
		return databaseError(c, err)
	}
	defer rows.Close()

	posts := make([]blogPost, 0)
	for rows.Next() {
		post, err := scanBlogPost(rows)
		if err != nil {
			return databaseError(c, err)
		}
		posts = append(posts, post)
	}
	if err := rows.Err(); err != nil {
		return databaseError(c, err)
	}
	return c.JSON(posts)
}

func (api *blogAPI) get(c fiber.Ctx) error {
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}
	ownerID, err := api.optionalUserID(c)
	if err != nil {
		return authError(c, err)
	}

	var post blogPost
	query := `
		SELECT id, slug, title, body, visibility, created_at, updated_at
		FROM blog_posts
		WHERE id = $1 AND (visibility = 'public'`
	args := []any{id}
	if ownerID != "" {
		query += " OR owner_id = $2"
		args = append(args, ownerID)
	}
	query += ")"
	if err := api.pool.QueryRow(c.Context(), query, args...).Scan(
		&post.ID, &post.Slug, &post.Title, &post.Body, &post.Visibility, &post.CreatedAt, &post.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return clientError(c, http.StatusNotFound, "post not found")
		}
		return databaseError(c, err)
	}
	return c.JSON(post)
}

func (api *blogAPI) create(c fiber.Ctx) error {
	claims, err := api.requiredClaims(c)
	if err != nil {
		return authError(c, err)
	}

	var input blogPostInput
	if err := c.Bind().Body(&input); err != nil {
		return clientError(c, http.StatusBadRequest, "invalid JSON body")
	}
	if input.Slug == nil || input.Title == nil || input.Body == nil {
		return clientError(c, http.StatusBadRequest, "slug, title, and body are required")
	}
	visibility := "private"
	if input.Visibility != nil {
		visibility = *input.Visibility
	}
	if err := validatePostFields(*input.Slug, *input.Title, *input.Body, visibility); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}

	var post blogPost
	err = api.pool.QueryRow(c.Context(), `
		INSERT INTO blog_posts (owner_id, slug, title, body, visibility)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, slug, title, body, visibility, created_at, updated_at`,
		claims.UserID, *input.Slug, *input.Title, *input.Body, visibility,
	).Scan(&post.ID, &post.Slug, &post.Title, &post.Body, &post.Visibility, &post.CreatedAt, &post.UpdatedAt)
	if err != nil {
		return databaseError(c, err)
	}
	return c.Status(http.StatusCreated).JSON(post)
}

func (api *blogAPI) update(c fiber.Ctx) error {
	claims, err := api.requiredClaims(c)
	if err != nil {
		return authError(c, err)
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}

	var input blogPostInput
	if err := c.Bind().Body(&input); err != nil {
		return clientError(c, http.StatusBadRequest, "invalid JSON body")
	}

	var post blogPost
	err = api.pool.QueryRow(c.Context(), `
		SELECT id, slug, title, body, visibility, created_at, updated_at
		FROM blog_posts WHERE id = $1 AND owner_id = $2`, id, claims.UserID).
		Scan(&post.ID, &post.Slug, &post.Title, &post.Body, &post.Visibility, &post.CreatedAt, &post.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	if err != nil {
		return databaseError(c, err)
	}

	if input.Slug != nil {
		post.Slug = *input.Slug
	}
	if input.Title != nil {
		post.Title = *input.Title
	}
	if input.Body != nil {
		post.Body = *input.Body
	}
	if input.Visibility != nil {
		post.Visibility = *input.Visibility
	}
	if err := validatePostFields(post.Slug, post.Title, post.Body, post.Visibility); err != nil {
		return clientError(c, http.StatusBadRequest, err.Error())
	}

	err = api.pool.QueryRow(c.Context(), `
		UPDATE blog_posts
		SET slug = $1, title = $2, body = $3, visibility = $4, updated_at = NOW()
		WHERE id = $5 AND owner_id = $6
		RETURNING id, slug, title, body, visibility, created_at, updated_at`,
		post.Slug, post.Title, post.Body, post.Visibility, id, claims.UserID,
	).Scan(&post.ID, &post.Slug, &post.Title, &post.Body, &post.Visibility, &post.CreatedAt, &post.UpdatedAt)
	if err != nil {
		return databaseError(c, err)
	}
	return c.JSON(post)
}

func (api *blogAPI) delete(c fiber.Ctx) error {
	claims, err := api.requiredClaims(c)
	if err != nil {
		return authError(c, err)
	}
	id, err := postID(c)
	if err != nil {
		return clientError(c, http.StatusBadRequest, "invalid post id")
	}
	result, err := api.pool.Exec(c.Context(), "DELETE FROM blog_posts WHERE id = $1 AND owner_id = $2", id, claims.UserID)
	if err != nil {
		return databaseError(c, err)
	}
	if result.RowsAffected() == 0 {
		return clientError(c, http.StatusNotFound, "post not found")
	}
	return c.SendStatus(http.StatusNoContent)
}

func (api *blogAPI) requiredClaims(c fiber.Ctx) (verify.Claims, error) {
	req, err := adaptor.ConvertRequest(c, true)
	if err != nil {
		return verify.Claims{}, err
	}
	claims, err := api.auth.Verifier().VerifyRequest(req)
	if err != nil {
		return verify.Claims{}, err
	}
	if claims.UserID == "" {
		return verify.Claims{}, errors.New("a user access token is required")
	}
	return claims, nil
}

func (api *blogAPI) optionalUserID(c fiber.Ctx) (string, error) {
	if strings.TrimSpace(c.Get("Authorization")) == "" {
		return "", nil
	}
	claims, err := api.requiredClaims(c)
	if err != nil {
		return "", err
	}
	return claims.UserID, nil
}

func scanBlogPost(row interface{ Scan(...any) error }) (blogPost, error) {
	var post blogPost
	err := row.Scan(&post.ID, &post.Slug, &post.Title, &post.Body, &post.Visibility, &post.CreatedAt, &post.UpdatedAt)
	return post, err
}

func postID(c fiber.Ctx) (int64, error) {
	return strconv.ParseInt(c.Params("id"), 10, 64)
}

func validatePostFields(slug, title, body, visibility string) error {
	if strings.TrimSpace(slug) == "" || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return errors.New("slug, title, and body cannot be empty")
	}
	if visibility != "public" && visibility != "private" {
		return errors.New("visibility must be public or private")
	}
	return nil
}

func authError(c fiber.Ctx, err error) error {
	return c.Status(http.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
}

func clientError(c fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": message})
}

func databaseError(c fiber.Ctx, err error) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "database error"})
}
