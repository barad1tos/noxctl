begin;

select 'notes';
select Z_PK, Z_OPT, ZVERSION, coalesce(ZMODIFICATIONDATE, 0),
       ZTRASHED, ZARCHIVED, ZPERMANENTLYDELETED
from ZSFNOTE
order by Z_PK;

select 'tags';
select Z_PK, Z_OPT, coalesce(ZMODIFICATIONDATE, 0),
       coalesce(ZTITLE, ''), coalesce(ZTAGCON, '')
from ZSFNOTETAG
order by Z_PK;

select 'links';
select Z_5NOTES, Z_13TAGS
from Z_5TAGS
order by Z_5NOTES, Z_13TAGS;

commit;
