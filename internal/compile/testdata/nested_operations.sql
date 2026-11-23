-- field: updatePostById
WITH __p_1 AS (
  SELECT "id" FROM "public"."users" WHERE "public"."users"."email" = $3
),
__mut AS (
  UPDATE "public"."posts" SET "title" = $2, "author_id" = __p_1."id" FROM __p_1 WHERE "public"."posts"."id" = $1 RETURNING "public"."posts".*
),
__c_2 AS (
  DELETE FROM "public"."comments" USING __mut WHERE "public"."comments"."id" = $4 AND "public"."comments"."post_id" = __mut."id" RETURNING "public"."comments".*
),
__c_3 AS (
  UPDATE "public"."comments" SET "body" = $6 FROM __mut WHERE "public"."comments"."id" = $5 AND "public"."comments"."post_id" = __mut."id" RETURNING "public"."comments".*
),
__vis_4 AS NOT MATERIALIZED (
  SELECT * FROM "public"."posts" WHERE NOT EXISTS (SELECT 1 FROM __mut AS __m WHERE __m."id" = "public"."posts"."id")
  UNION ALL SELECT * FROM __mut
),
__vis_5 AS NOT MATERIALIZED (
  SELECT * FROM "public"."comments" WHERE NOT EXISTS (SELECT 1 FROM __c_2 AS __m WHERE __m."id" = "public"."comments"."id") AND NOT EXISTS (SELECT 1 FROM __c_3 AS __m WHERE __m."id" = "public"."comments"."id")
  UNION ALL SELECT * FROM __c_3
)
SELECT __guarded.data
FROM (
  SELECT json_build_object('post', __row.data) AS data
  FROM (
    SELECT json_build_object('title', __t_1."title", 'author', __l_3.data, 'commentsByPostId', __l_7.data) AS data
    FROM __mut AS __t_1
    LEFT JOIN LATERAL (
      SELECT json_build_object('email', __t_2."email") AS data
      FROM "public"."users" AS __t_2
      WHERE __t_2."id" = __t_1."author_id"
      LIMIT 1
    ) AS __l_3 ON true
    LEFT JOIN LATERAL (
      SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_5 AS __c_6
      WHERE __c_6."post_id" = __t_1."id")::int, 'nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $8), '[]'::json)) AS data
      FROM (
        SELECT json_build_object('body', __t_4."body") AS ndata, row_number() OVER (ORDER BY __t_4."id" ASC) AS __rn
        FROM __vis_5 AS __t_4
        WHERE __t_4."post_id" = __t_1."id"
        ORDER BY __t_4."id" ASC
        LIMIT $7
      ) AS __s_5
      GROUP BY ()
    ) AS __l_7 ON true
  ) AS __row
) AS __guarded
WHERE (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: author.connectByEmail matched ', count(*), ' rows')::boolean END FROM __p_1)
  AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: commentsByPostId.deleteById[0] matched ', count(*), ' rows')::boolean END FROM __c_2)
  AND (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: commentsByPostId.updateByNodeId[0] matched ', count(*), ' rows')::boolean END FROM __c_3)
-- args: [1,"T","ada@example.com",3,"2","edited",101,100]

-- field: updateCommentById
WITH __mut AS (
  UPDATE "public"."comments" SET "parent_id" = CASE WHEN "public"."comments"."parent_id" = $2 THEN NULL ELSE "public"."comments"."parent_id" END WHERE "public"."comments"."id" = $1 RETURNING "public"."comments".*
),
__c_1 AS (
  UPDATE "public"."comments" SET "parent_id" = __mut."id" FROM __mut WHERE "public"."comments"."id" = $3 RETURNING "public"."comments".*
),
__c_2 AS (
  UPDATE "public"."comments" SET "parent_id" = NULL FROM __mut WHERE "public"."comments"."id" = $4 AND "public"."comments"."parent_id" = __mut."id" RETURNING "public"."comments".*
),
__vis_3 AS NOT MATERIALIZED (
  SELECT * FROM "public"."comments" WHERE NOT EXISTS (SELECT 1 FROM __mut AS __m WHERE __m."id" = "public"."comments"."id") AND NOT EXISTS (SELECT 1 FROM __c_1 AS __m WHERE __m."id" = "public"."comments"."id") AND NOT EXISTS (SELECT 1 FROM __c_2 AS __m WHERE __m."id" = "public"."comments"."id")
  UNION ALL SELECT * FROM __mut
  UNION ALL SELECT * FROM __c_1
  UNION ALL SELECT * FROM __c_2
)
SELECT __guarded.data
FROM (
  SELECT json_build_object('comment', __row.data) AS data
  FROM (
    SELECT json_build_object('parentId', __t_1."parent_id", 'commentsByParentId', __l_4.data) AS data
    FROM __mut AS __t_1
    LEFT JOIN LATERAL (
      SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $6), '[]'::json)) AS data
      FROM (
        SELECT json_build_object('id', __t_2."id") AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
        FROM __vis_3 AS __t_2
        WHERE __t_2."parent_id" = __t_1."id"
        ORDER BY __t_2."id" ASC
        LIMIT $5
      ) AS __s_3
      GROUP BY ()
    ) AS __l_4 ON true
  ) AS __row
) AS __guarded
WHERE (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: commentsByParentId.connectById[0] matched ', count(*), ' rows')::boolean END FROM __c_1)
-- args: [5,4,6,7,101,100]

-- field: updateUserById
WITH __mut AS (
  SELECT "public"."users".* FROM "public"."users" WHERE "public"."users"."id" = $1
),
__c_1 AS (
  INSERT INTO "public"."places" ("name", "owner_id") SELECT $2, __mut."id" FROM __mut RETURNING *
),
__vis_2 AS NOT MATERIALIZED (
  SELECT * FROM "public"."places"
  UNION ALL SELECT * FROM __c_1
)
SELECT json_build_object('user', __row.data) AS data
FROM (
  SELECT json_build_object('placesByOwnerId', __l_5.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_2 AS __c_4
    WHERE __c_4."owner_id" = __t_1."id")::int) AS data
    FROM (
      SELECT row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM __vis_2 AS __t_2
      WHERE __t_2."owner_id" = __t_1."id"
      ORDER BY __t_2."id" ASC
      LIMIT $3
    ) AS __s_3
    GROUP BY ()
  ) AS __l_5 ON true
) AS __row
-- args: [1,"Home",101]

-- field: createPost
WITH __p_1 AS (
  UPDATE "public"."users" SET "full_name" = $3 WHERE "public"."users"."id" = $2 RETURNING "public"."users".*
),
__mut AS (
  INSERT INTO "public"."posts" ("title", "author_id") SELECT $1, __p_1."id" FROM __p_1 RETURNING *
),
__vis_2 AS NOT MATERIALIZED (
  SELECT * FROM "public"."users" WHERE NOT EXISTS (SELECT 1 FROM __p_1 AS __m WHERE __m."id" = "public"."users"."id")
  UNION ALL SELECT * FROM __p_1
),
__vis_3 AS NOT MATERIALIZED (
  SELECT * FROM "public"."posts"
  UNION ALL SELECT * FROM __mut
)
SELECT __guarded.data
FROM (
  SELECT json_build_object('post', __row.data) AS data
  FROM (
    SELECT json_build_object('author', __l_3.data) AS data
    FROM __mut AS __t_1
    LEFT JOIN LATERAL (
      SELECT json_build_object('fullName', __t_2."full_name") AS data
      FROM __vis_2 AS __t_2
      WHERE __t_2."id" = __t_1."author_id"
      LIMIT 1
    ) AS __l_3 ON true
  ) AS __row
) AS __guarded
WHERE (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: author.updateById matched ', count(*), ' rows')::boolean END FROM __p_1)
-- args: ["x",2,"G"]

