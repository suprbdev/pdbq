-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_8.ndata ORDER BY __s_8.__rn ASC) FILTER (WHERE __s_8.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'email', __t_1."email", 'postsByAuthorId', __l_7.data) AS ndata, __t_1.__rn
  FROM (
    SELECT __t_1."id", __t_1."email", row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
    FROM "public"."users" AS __t_1
    ORDER BY __t_1."id" ASC
    LIMIT 3
  ) AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_6
    WHERE __c_6."author_id" = __t_1."id")::int, 'nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $1), '[]'::json)) AS data
    FROM (
      SELECT json_build_object('id', __t_2."id", 'title', __t_2."title", 'author', __l_4.data) AS ndata, __t_2.__rn
      FROM (
        SELECT __t_2."id", __t_2."author_id", __t_2."title", row_number() OVER (ORDER BY __t_2."id" DESC) AS __rn
        FROM "public"."posts" AS __t_2
        WHERE __t_2."author_id" = __t_1."id"
        ORDER BY __t_2."id" DESC
        LIMIT 6
      ) AS __t_2
      LEFT JOIN LATERAL (
        SELECT json_build_object('id', __t_3."id", 'email', __t_3."email") AS data
        FROM "public"."users" AS __t_3
        WHERE __t_3."id" = __t_2."author_id"
        LIMIT 1
      ) AS __l_4 ON true
    ) AS __s_5
    GROUP BY ()
  ) AS __l_7 ON true
) AS __s_8
GROUP BY ()
-- args: [5,2]

